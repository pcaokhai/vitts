package http

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/pcaokhai/vitts/gateway/internal/telemetry"
)

// Metrics serves the Prometheus exposition format.
//
// It is mounted outside the tenant API and carries no credential: the endpoint is
// reachable only on the private network, and requiring a key would mean giving
// Prometheus one (docs/07-permissions.md).
func Metrics(metrics *telemetry.Metrics) http.Handler {
	return promhttp.HandlerFor(metrics.Registry(), promhttp.HandlerOpts{
		// A broken collector must not take the endpoint down: report what works and
		// surface the rest through promhttp_metric_handler_errors_total.
		ErrorHandling: promhttp.ContinueOnError,
	})
}

// Observe records RED metrics for every request.
//
// The route label is chi's pattern, not the raw path: `/v1/jobs/{jobId}` keeps one
// series while `/v1/jobs/<uuid>` would create one per job.
func Observe(metrics *telemetry.Metrics) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			started := time.Now()
			recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

			next.ServeHTTP(recorder, r)

			route := routePattern(r)
			metrics.RequestsInFlight.WithLabelValues(route).Inc()
			defer metrics.RequestsInFlight.WithLabelValues(route).Dec()
			metrics.ObserveRequest(route, r.Method, recorder.status, time.Since(started))
		})
	}
}

// routePattern is the matched template, or "unmatched" when nothing matched — a 404
// flood must not become a flood of label values.
func routePattern(r *http.Request) string {
	if ctx := chi.RouteContext(r.Context()); ctx != nil {
		if pattern := ctx.RoutePattern(); pattern != "" {
			return pattern
		}
	}
	return "unmatched"
}
