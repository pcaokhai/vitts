package http

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/pcaokhai/vitts/gateway/internal/auth"
	"github.com/pcaokhai/vitts/gateway/internal/telemetry"
	"github.com/pcaokhai/vitts/gateway/internal/tenants"
)

// Deps are the collaborators the router mounts. Adding a nil-able field here is how a
// task in progress mounts nothing rather than mounting a half-wired route.
type Deps struct {
	Logger    zerolog.Logger
	Readiness *Readiness
	Admin     *AdminGuard
	Tenants   *tenants.Service
	// Auth, Limiter and Plans protect the tenant API under /v1.
	Auth    *auth.Authenticator
	Limiter RateLimiter
	Plans   PlanLimits
	// TenantAPI are the /v1 routes. Handlers arrive with the tasks that own them
	// (1.9 synthesize, 1.13 voices, 2.3 jobs); passing them here is what puts them
	// behind the guard chain, so a route cannot be registered outside it.
	TenantAPI []Route
}

// Route is one tenant endpoint.
type Route struct {
	Method  string
	Pattern string
	Handler http.HandlerFunc
	// Scope the key must carry, or "" when the endpoint needs none beyond a valid key.
	Scope auth.Scope
}

// Router builds the gateway's HTTP handler.
//
// Middleware order is load-bearing: the request id exists before anything logs, the
// logger is on the context before recovery needs it, and tracing wraps the whole chain so
// a span covers the failure path too.
func Router(deps Deps) http.Handler {
	mux := chi.NewRouter()

	mux.Use(RequestID)
	mux.Use(Logger(deps.Logger))
	mux.Use(Recover)

	mux.Get("/healthz", Healthz())
	mux.Get("/readyz", Readyz(deps.Readiness))

	if deps.Admin != nil && deps.Tenants != nil {
		mux.Mount("/admin", AdminRoutes(deps.Admin, deps.Tenants))
	}
	if deps.Auth != nil && deps.Limiter != nil && deps.Plans != nil {
		mux.Mount("/v1", TenantRoutes(deps))
	}

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

// TenantRoutes is the authenticated tenant API.
//
// Order is load-bearing: authenticate before rate limiting, because the bucket is per key
// and its size comes from the tenant's plan, and a scope check last so a key that is
// valid but unauthorised still spends its budget rather than probing for free.
//
// chi runs middleware only for matched routes, so an unmatched /v1 path is a plain 404
// and the chain protects exactly the routes registered here — which is why routes are
// passed in rather than registered elsewhere.
func TenantRoutes(deps Deps) http.Handler {
	r := chi.NewRouter()
	r.Use(Authenticate(deps.Auth))
	r.Use(RateLimit(deps.Limiter, deps.Plans))

	for _, route := range deps.TenantAPI {
		handler := http.Handler(route.Handler)
		if route.Scope != "" {
			handler = RequireScope(route.Scope)(handler)
		}
		r.Method(route.Method, route.Pattern, handler)
	}

	return r
}
