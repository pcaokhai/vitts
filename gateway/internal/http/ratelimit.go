package http

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/pcaokhai/vitts/gateway/internal/degrade"
	"github.com/pcaokhai/vitts/gateway/internal/ratelimit"
	"github.com/pcaokhai/vitts/gateway/internal/telemetry"
)

// degradeRetryAfter is what a client is told once the limiter has been unreachable for
// longer than the degrade window. Short, because Redis usually comes back.
const degradeRetryAfter = 5 * time.Second

// Rate limit headers (US-06 acceptance criterion 1).
const (
	HeaderRetryAfter        = "Retry-After"
	HeaderRateLimitLimit    = "X-RateLimit-Limit"
	HeaderRateLimitRemain   = "X-RateLimit-Remaining"
	HeaderRateLimitResetSec = "X-RateLimit-Reset"
)

// RateLimiter is the port the middleware needs.
type RateLimiter interface {
	Allow(ctx context.Context, keyID uuid.UUID, perMinute int32) (ratelimit.RateDecision, error)
}

// PlanLimits answers what a plan allows. Implemented by the Postgres adapter and cached
// there; the middleware must not learn about SQL.
type PlanLimits interface {
	ReqPerMinute(ctx context.Context, planID string) (int32, error)
}

// RateLimit enforces the per-key request rate.
//
// It runs after Authenticate, because the bucket is per key and its size comes from the
// tenant's plan. An unauthenticated request is rejected before it can cost Redis a call.
func RateLimit(limiter RateLimiter, limits PlanLimits, metrics *telemetry.Metrics) func(http.Handler) http.Handler {
	return rateLimit(limiter, limits, metrics, degrade.New(degrade.Window, nil))
}

func rateLimit(
	limiter RateLimiter, limits PlanLimits, metrics *telemetry.Metrics, breaker *degrade.Breaker,
) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			identity, ok := IdentityFrom(r.Context())
			if !ok {
				WriteProblem(w, r, NewError(CodeUnauthorized, "missing or malformed API key"))
				return
			}

			perMinute, err := limits.ReqPerMinute(r.Context(), identity.PlanID)
			if err != nil {
				WriteProblem(w, r, &Error{Code: CodeInternal, Cause: err})
				return
			}

			decision, err := limiter.Allow(r.Context(), identity.KeyID, perMinute)
			if err != nil {
				// Neither failing closed nor failing open is right on its own: one makes
				// Redis a single point of failure for the whole API, the other turns an
				// outage into an unbounded load test. Serve for the degrade window, then
				// refuse (ADR-011).
				if !breaker.Tolerate() {
					zerolog.Ctx(r.Context()).Error().Err(err).
						Msg("rate limiter unavailable beyond the degrade window")
					WriteProblem(w, r, &Error{
						Code:       CodeOverloaded,
						Detail:     "rate limiting is unavailable",
						RetryAfter: degradeRetryAfter,
						Cause:      err,
					})
					return
				}
				zerolog.Ctx(r.Context()).Warn().Err(err).Msg("rate limiter unavailable, serving degraded")
				if metrics != nil {
					metrics.DegradedTotal.WithLabelValues("ratelimit").Inc()
				}
				next.ServeHTTP(w, r)
				return
			}
			breaker.Succeed()

			writeRateHeaders(w, decision)
			if !decision.Allowed {
				WriteProblem(w, r, NewError(CodeRateLimited, "request rate exceeded"))
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

func writeRateHeaders(w http.ResponseWriter, decision ratelimit.RateDecision) {
	w.Header().Set(HeaderRateLimitLimit, strconv.Itoa(decision.Limit))
	w.Header().Set(HeaderRateLimitRemain, strconv.Itoa(decision.Remaining))
	if decision.Allowed {
		return
	}

	seconds := retryAfterSeconds(decision.RetryAfter)
	w.Header().Set(HeaderRetryAfter, seconds)
	w.Header().Set(HeaderRateLimitResetSec, seconds)
}
