package http

import (
	"context"
	"net/http"
	"strconv"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/pcaokhai/vitts/gateway/internal/ratelimit"
)

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
func RateLimit(limiter RateLimiter, limits PlanLimits) func(http.Handler) http.Handler {
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
				// Fail closed: a limiter we cannot consult is not permission to ignore
				// the limit, or one Redis outage becomes an unbounded load test.
				zerolog.Ctx(r.Context()).Error().Err(err).Msg("rate limiter unavailable")
				WriteProblem(w, r, &Error{Code: CodeInternal, Cause: err})
				return
			}

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
