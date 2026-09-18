package http

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/pcaokhai/vitts/gateway/internal/telemetry"
)

// Router builds the gateway's HTTP handler.
//
// Middleware order is load-bearing: the request id exists before anything logs, the
// logger is on the context before recovery needs it, and tracing wraps the whole chain so
// a span covers the failure path too.
func Router(logger zerolog.Logger, readiness *Readiness) http.Handler {
	mux := chi.NewRouter()

	mux.Use(RequestID)
	mux.Use(Logger(logger))
	mux.Use(Recover)

	mux.Get("/healthz", Healthz())
	mux.Get("/readyz", Readyz(readiness))

	// Routing failures are protocol-level, so they keep their own status. The code
	// stays `invalid_request` because that is the closest member of the Problem enum in
	// docs/api/openapi.yaml, and a new code goes in the contract first.
	mux.NotFound(func(w http.ResponseWriter, r *http.Request) {
		WriteProblem(w, r, &Error{
			Code:   CodeInvalidRequest,
			Status: http.StatusNotFound,
			Title:  "No such endpoint",
		})
	})
	mux.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		WriteProblem(w, r, &Error{
			Code:   CodeInvalidRequest,
			Status: http.StatusMethodNotAllowed,
			Title:  "Method not allowed",
		})
	})

	return otelhttp.NewHandler(mux, telemetry.ServiceName)
}
