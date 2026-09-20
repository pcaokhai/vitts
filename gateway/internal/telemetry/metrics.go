package telemetry

import (
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

// Metrics is the gateway's instrument set (US-18 acceptance criterion 1).
//
// Everything lives in one struct rather than package-level globals, so tests get a fresh
// registry and nothing depends on init order (.claude/rules/gateway-go.md: no
// package-level mutable state).
type Metrics struct {
	registry *prometheus.Registry

	// RED per route.
	RequestsTotal    *prometheus.CounterVec
	RequestDuration  *prometheus.HistogramVec
	RequestsInFlight *prometheus.GaugeVec

	// Synthesis quality and speed.
	TTFASeconds *prometheus.HistogramVec
	RTF         *prometheus.HistogramVec

	// Admission control.
	QueueDepth       *prometheus.GaugeVec
	QueueWaitSeconds *prometheus.HistogramVec
	Overloaded       *prometheus.CounterVec

	// Cache and billing.
	CacheTotal      *prometheus.CounterVec
	UsageCharsTotal *prometheus.CounterVec

	// Fleet.
	WorkerSlotsBusy  *prometheus.GaugeVec
	WorkerSlotsTotal *prometheus.GaugeVec
	WorkerReady      *prometheus.GaugeVec

	// Jobs.
	JobsTotal       *prometheus.CounterVec
	JobDuration     *prometheus.HistogramVec
	WebhookAttempts *prometheus.CounterVec
}

// Latency buckets are chosen around the targets they police: NFR-01 puts streaming TTFA
// at p95 ≤ 300 ms, so the interesting resolution is below a second, and a few coarse
// buckets above it show when things have gone badly wrong.
var (
	latencyBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.2, 0.3, 0.5, 1, 2, 5, 10, 30, 60}
	ttfaBuckets    = []float64{0.02, 0.05, 0.1, 0.15, 0.2, 0.3, 0.45, 0.6, 1, 2, 5}
	// RTF below 1 is faster than real time; above 1 the fleet cannot keep up.
	rtfBuckets = []float64{0.1, 0.2, 0.3, 0.4, 0.5, 0.7, 1, 1.5, 2, 4}
	// Queue wait is what ADR-007's budgets bound: 2 s for streams, 10 s for sync.
	waitBuckets = []float64{0.001, 0.01, 0.05, 0.1, 0.25, 0.5, 1, 2, 5, 10, 30}
)

// NewMetrics builds the instrument set and its registry.
func NewMetrics() *Metrics {
	registry := prometheus.NewRegistry()
	factory := promauto{registry}

	m := &Metrics{
		registry: registry,

		RequestsTotal: factory.counterVec(prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "HTTP requests by route, method and status class.",
		}, []string{"route", "method", "status"}),

		RequestDuration: factory.histogramVec(prometheus.HistogramOpts{
			Name:    "http_request_duration_seconds",
			Help:    "HTTP request duration by route.",
			Buckets: latencyBuckets,
		}, []string{"route", "method"}),

		RequestsInFlight: factory.gaugeVec(prometheus.GaugeOpts{
			Name: "http_requests_in_flight",
			Help: "HTTP requests currently being served, by route.",
		}, []string{"route"}),

		TTFASeconds: factory.histogramVec(prometheus.HistogramOpts{
			Name:    "tts_ttfa_seconds",
			Help:    "Time to first audio frame, by mode (NFR-01).",
			Buckets: ttfaBuckets,
		}, []string{"mode"}),

		RTF: factory.histogramVec(prometheus.HistogramOpts{
			Name:    "tts_rtf",
			Help:    "Real-time factor: synthesis wall time divided by audio duration.",
			Buckets: rtfBuckets,
		}, []string{"mode"}),

		QueueDepth: factory.gaugeVec(prometheus.GaugeOpts{
			Name: "dispatch_queue_depth",
			Help: "Callers waiting for a dispatch slot, by priority class (ADR-007).",
		}, []string{"class"}),

		QueueWaitSeconds: factory.histogramVec(prometheus.HistogramOpts{
			Name:    "dispatch_queue_wait_seconds",
			Help:    "How long a request waited for a slot, by class.",
			Buckets: waitBuckets,
		}, []string{"class"}),

		Overloaded: factory.counterVec(prometheus.CounterOpts{
			Name: "dispatch_overloaded_total",
			Help: "Requests refused because no slot became free inside the class budget.",
		}, []string{"class"}),

		CacheTotal: factory.counterVec(prometheus.CounterOpts{
			Name: "cache_total",
			Help: "Audio cache lookups by result.",
		}, []string{"result"}),

		UsageCharsTotal: factory.counterVec(prometheus.CounterOpts{
			Name: "usage_chars_total",
			Help: "Characters billed, by tenant and mode.",
		}, []string{"tenant", "mode"}),

		WorkerSlotsBusy: factory.gaugeVec(prometheus.GaugeOpts{
			Name: "worker_slots_busy",
			Help: "Inference slots in use, by worker.",
		}, []string{"worker"}),

		WorkerSlotsTotal: factory.gaugeVec(prometheus.GaugeOpts{
			Name: "worker_slots_total",
			Help: "Inference slots advertised, by worker.",
		}, []string{"worker"}),

		WorkerReady: factory.gaugeVec(prometheus.GaugeOpts{
			Name: "worker_ready",
			Help: "1 when a worker is ready and not ejected, 0 otherwise.",
		}, []string{"worker"}),

		JobsTotal: factory.counterVec(prometheus.CounterOpts{
			Name: "jobs_total",
			Help: "Jobs by terminal status.",
		}, []string{"status"}),

		JobDuration: factory.histogramVec(prometheus.HistogramOpts{
			Name:    "job_duration_seconds",
			Help:    "Wall time from job creation to a terminal state.",
			Buckets: []float64{1, 5, 15, 30, 60, 120, 300, 600, 1800, 3600},
		}, []string{"status"}),

		WebhookAttempts: factory.counterVec(prometheus.CounterOpts{
			Name: "webhook_attempts_total",
			Help: "Webhook delivery attempts by outcome.",
		}, []string{"outcome"}),
	}

	// Process and Go runtime metrics: the first questions asked during an incident are
	// usually about memory and goroutines, not about us.
	registry.MustRegister(
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		collectors.NewGoCollector(),
	)

	return m
}

// Registry exposes the registry for the /metrics handler.
func (m *Metrics) Registry() *prometheus.Registry { return m.registry }

// ObserveRequest records one HTTP request.
//
// The status is bucketed to its class: a per-status-code label would multiply series for
// no operational gain, since alerting cares about 5xx rate, not about 502 versus 503.
func (m *Metrics) ObserveRequest(route, method string, status int, duration time.Duration) {
	m.RequestsTotal.WithLabelValues(route, method, statusClass(status)).Inc()
	m.RequestDuration.WithLabelValues(route, method).Observe(duration.Seconds())
}

func statusClass(status int) string {
	switch {
	case status >= 500:
		return "5xx"
	case status >= 400:
		return "4xx"
	case status >= 300:
		return "3xx"
	default:
		return "2xx"
	}
}

// promauto registers each collector as it is built, so a typo in a name fails at boot
// rather than producing a metric nobody scrapes.
type promauto struct{ registry *prometheus.Registry }

func (p promauto) counterVec(opts prometheus.CounterOpts, labels []string) *prometheus.CounterVec {
	collector := prometheus.NewCounterVec(opts, labels)
	p.registry.MustRegister(collector)
	return collector
}

func (p promauto) gaugeVec(opts prometheus.GaugeOpts, labels []string) *prometheus.GaugeVec {
	collector := prometheus.NewGaugeVec(opts, labels)
	p.registry.MustRegister(collector)
	return collector
}

func (p promauto) histogramVec(opts prometheus.HistogramOpts, labels []string) *prometheus.HistogramVec {
	collector := prometheus.NewHistogramVec(opts, labels)
	p.registry.MustRegister(collector)
	return collector
}

// TenantLabel bounds tenant cardinality.
//
// usage_chars_total is per tenant by contract (US-18), and an unbounded tenant label is
// the classic way a metrics backend falls over. A uuid is already bounded in length; the
// guard here is against an empty or malformed value becoming its own series.
func TenantLabel(tenantID string) string {
	const uuidLen = 36
	if len(tenantID) != uuidLen {
		return "unknown"
	}
	return tenantID
}

// Seconds is a small helper so callers do not repeat the conversion.
func Seconds(d time.Duration) float64 { return d.Seconds() }

// MillisecondsToSeconds converts the millisecond figures the domain uses.
func MillisecondsToSeconds(ms int32) float64 { return float64(ms) / 1000 }

// FormatStatus renders an HTTP status for a label.
func FormatStatus(status int) string { return strconv.Itoa(status) }
