package telemetry_test

import (
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"github.com/pcaokhai/vitts/gateway/internal/telemetry"
)

// US-18 acceptance criterion 1 names these by hand; this test is what keeps a rename
// from silently breaking a dashboard or an alert rule.
func TestContractMetricsExist(t *testing.T) {
	t.Parallel()

	metrics := telemetry.NewMetrics()
	metrics.ObserveRequest("/v1/synthesize", "POST", 200, 150*time.Millisecond)
	metrics.TTFASeconds.WithLabelValues("stream").Observe(0.08)
	metrics.RTF.WithLabelValues("sync").Observe(0.35)
	metrics.QueueDepth.WithLabelValues("stream").Set(3)
	metrics.QueueWaitSeconds.WithLabelValues("sync").Observe(0.4)
	metrics.CacheTotal.WithLabelValues("hit").Inc()
	metrics.UsageCharsTotal.WithLabelValues("6f1c0a2e-0000-4000-8000-000000000000", "sync").Add(42)
	metrics.WorkerSlotsBusy.WithLabelValues("worker:50051").Set(1)

	families, err := metrics.Registry().Gather()
	require.NoError(t, err)

	present := map[string]bool{}
	for _, family := range families {
		present[family.GetName()] = true
	}

	for _, name := range []string{
		"http_requests_total",
		"http_request_duration_seconds",
		"tts_ttfa_seconds",
		"tts_rtf",
		"dispatch_queue_depth",
		"dispatch_queue_wait_seconds",
		"cache_total",
		"usage_chars_total",
		"worker_slots_busy",
	} {
		require.True(t, present[name], "the contract names %q", name)
	}
}

func TestStatusIsBucketedByClass(t *testing.T) {
	t.Parallel()

	metrics := telemetry.NewMetrics()
	metrics.ObserveRequest("/v1/jobs", "POST", 202, time.Millisecond)
	metrics.ObserveRequest("/v1/jobs", "POST", 204, time.Millisecond)
	metrics.ObserveRequest("/v1/jobs", "POST", 500, time.Millisecond)

	// Alerting cares about the 5xx rate, not about 502 versus 503; per-code labels would
	// multiply series for no operational gain.
	require.InDelta(t, 2,
		testutil.ToFloat64(metrics.RequestsTotal.WithLabelValues("/v1/jobs", "POST", "2xx")), 0.001)
	require.InDelta(t, 1,
		testutil.ToFloat64(metrics.RequestsTotal.WithLabelValues("/v1/jobs", "POST", "5xx")), 0.001)
}

// An unbounded tenant label is the classic way a metrics backend falls over.
func TestTenantLabelIsBounded(t *testing.T) {
	t.Parallel()

	require.Equal(t, "6f1c0a2e-0000-4000-8000-000000000000",
		telemetry.TenantLabel("6f1c0a2e-0000-4000-8000-000000000000"))
	require.Equal(t, "unknown", telemetry.TenantLabel(""))
	require.Equal(t, "unknown", telemetry.TenantLabel(strings.Repeat("x", 500)))
}

func TestRegistryCarriesProcessAndRuntimeMetrics(t *testing.T) {
	t.Parallel()

	families, err := telemetry.NewMetrics().Registry().Gather()
	require.NoError(t, err)

	present := map[string]bool{}
	for _, family := range families {
		present[family.GetName()] = true
	}

	// The first questions in an incident are usually about memory and goroutines.
	require.True(t, present["go_goroutines"])
	require.True(t, present["go_memstats_alloc_bytes"])
}
