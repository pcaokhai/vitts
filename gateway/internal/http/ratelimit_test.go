package http_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/pcaokhai/vitts/gateway/internal/auth"
	"github.com/pcaokhai/vitts/gateway/internal/degrade"
	gatewayhttp "github.com/pcaokhai/vitts/gateway/internal/http"
	"github.com/pcaokhai/vitts/gateway/internal/ratelimit"
)

type stubLimiter struct {
	decision ratelimit.RateDecision
	err      error
	calls    int
}

func (s *stubLimiter) Allow(context.Context, uuid.UUID, int32) (ratelimit.RateDecision, error) {
	s.calls++
	return s.decision, s.err
}

type stubPlans struct {
	perMinute int32
	err       error
}

func (s stubPlans) ReqPerMinute(context.Context, string) (int32, error) {
	return s.perMinute, s.err
}

func limited(limiter gatewayhttp.RateLimiter, plans gatewayhttp.PlanLimits) http.Handler {
	final := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	repo := repoFunc(func(context.Context, []byte) (auth.Identity, error) {
		return liveIdentity(auth.ScopeSynth), nil
	})
	handler := gatewayhttp.Authenticate(auth.NewAuthenticator(repo))(
		gatewayhttp.RateLimit(limiter, plans, nil)(final))
	return gatewayhttp.RequestID(gatewayhttp.Logger(zerolog.New(&strings.Builder{}))(handler))
}

func limitedWithBreaker(
	limiter gatewayhttp.RateLimiter, plans gatewayhttp.PlanLimits, breaker *degrade.Breaker,
) http.Handler {
	final := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	repo := repoFunc(func(context.Context, []byte) (auth.Identity, error) {
		return liveIdentity(auth.ScopeSynth), nil
	})
	handler := gatewayhttp.Authenticate(auth.NewAuthenticator(repo))(
		gatewayhttp.RateLimitWithBreaker(limiter, plans, nil, breaker)(final))
	return gatewayhttp.RequestID(gatewayhttp.Logger(zerolog.New(&strings.Builder{}))(handler))
}

func authedRequest() *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/v1/synthesize", nil)
	req.Header.Set("Authorization", "Bearer "+goodSecret)
	return req
}

func TestRateLimitPassesAndReportsBudget(t *testing.T) {
	t.Parallel()

	limiter := &stubLimiter{decision: ratelimit.RateDecision{Allowed: true, Limit: 60, Remaining: 59}}

	res := do(t, limited(limiter, stubPlans{perMinute: 60}), authedRequest())

	require.Equal(t, http.StatusOK, res.Code)
	require.Equal(t, "60", res.Header().Get(gatewayhttp.HeaderRateLimitLimit))
	require.Equal(t, "59", res.Header().Get(gatewayhttp.HeaderRateLimitRemain))
	require.Empty(t, res.Header().Get(gatewayhttp.HeaderRetryAfter))
}

func TestRateLimitRefusesWithRetryAfter(t *testing.T) {
	t.Parallel()

	limiter := &stubLimiter{decision: ratelimit.RateDecision{
		Limit: 60, Remaining: 0, RetryAfter: 1500 * time.Millisecond,
	}}

	res := do(t, limited(limiter, stubPlans{perMinute: 60}), authedRequest())

	require.Equal(t, http.StatusTooManyRequests, res.Code)
	require.Equal(t, gatewayhttp.CodeRateLimited, decodeProblem(t, res).Code)
	require.Equal(t, "2", res.Header().Get(gatewayhttp.HeaderRetryAfter), "rounded up to whole seconds")
	require.Equal(t, "0", res.Header().Get(gatewayhttp.HeaderRateLimitRemain))
}

func TestRetryAfterIsNeverZero(t *testing.T) {
	t.Parallel()

	limiter := &stubLimiter{decision: ratelimit.RateDecision{
		Limit: 60, RetryAfter: 10 * time.Millisecond,
	}}

	res := do(t, limited(limiter, stubPlans{perMinute: 60}), authedRequest())

	require.Equal(t, "1", res.Header().Get(gatewayhttp.HeaderRetryAfter),
		"Retry-After 0 would invite an immediate retry into the same refusal")
}

// T-106. A limiter outage degrades for a bounded window and then refuses. Failing closed
// immediately made Redis a single point of failure for the whole API; failing open
// forever would serve unlimited traffic (ADR-011).
func TestRateLimitDegradesForAWindowThenRefuses(t *testing.T) {
	t.Parallel()

	now := time.Unix(0, 0)
	limiter := &stubLimiter{err: errors.New("dial tcp: connection refused")}
	handler := limitedWithBreaker(limiter, stubPlans{perMinute: 60},
		gatewayhttp.NewBreaker(60*time.Second, func() time.Time { return now }))

	res := do(t, handler, authedRequest())
	require.Equal(t, http.StatusOK, res.Code, "a brief Redis outage must not take the API down")

	now = now.Add(61 * time.Second)

	res = do(t, handler, authedRequest())
	require.Equal(t, http.StatusServiceUnavailable, res.Code,
		"past the window the gateway stops serving unlimited traffic")
	require.NotEmpty(t, res.Header().Get(gatewayhttp.HeaderRetryAfter))
	require.NotContains(t, res.Body.String(), "connection refused")
}

func TestRateLimitFailsWhenThePlanCannotBeRead(t *testing.T) {
	t.Parallel()

	limiter := &stubLimiter{decision: ratelimit.RateDecision{Allowed: true}}

	res := do(t, limited(limiter, stubPlans{err: errors.New("no such plan")}), authedRequest())

	require.Equal(t, http.StatusInternalServerError, res.Code)
	require.Zero(t, limiter.calls, "no plan means no budget to spend")
}

func TestRateLimitRequiresAuthentication(t *testing.T) {
	t.Parallel()

	limiter := &stubLimiter{decision: ratelimit.RateDecision{Allowed: true}}
	handler := gatewayhttp.RequestID(gatewayhttp.RateLimit(limiter, stubPlans{perMinute: 60}, nil)(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })))

	res := do(t, handler, httptest.NewRequest(http.MethodPost, "/v1/synthesize", nil))

	require.Equal(t, http.StatusUnauthorized, res.Code)
	require.Zero(t, limiter.calls)
}

// TestTenantAPIIsGuardedEndToEnd exercises the real /v1 chain: a route registered through
// Deps must sit behind authentication, the rate limiter and its scope check.
func TestTenantAPIIsGuardedEndToEnd(t *testing.T) {
	t.Parallel()

	reached := 0
	limiter := &stubLimiter{decision: ratelimit.RateDecision{Allowed: true, Limit: 60, Remaining: 59}}
	repo := repoFunc(func(context.Context, []byte) (auth.Identity, error) {
		return liveIdentity(auth.ScopeSynth), nil
	})

	handler := gatewayhttp.Router(gatewayhttp.Deps{
		Logger:    zerolog.New(&strings.Builder{}),
		Readiness: gatewayhttp.NewReadiness(),
		Auth:      auth.NewAuthenticator(repo),
		Limiter:   limiter,
		Plans:     stubPlans{perMinute: 60},
		TenantAPI: []gatewayhttp.Route{
			{
				Method: http.MethodGet, Pattern: "/voices", Scope: auth.ScopeSynth,
				Handler: func(w http.ResponseWriter, _ *http.Request) {
					reached++
					w.WriteHeader(http.StatusOK)
				},
			},
			{
				Method: http.MethodGet, Pattern: "/usage", Scope: auth.ScopeUsage,
				Handler: func(w http.ResponseWriter, _ *http.Request) {
					reached++
					w.WriteHeader(http.StatusOK)
				},
			},
		},
	})

	t.Run("no credential", func(t *testing.T) {
		res := do(t, handler, httptest.NewRequest(http.MethodGet, "/v1/voices", nil))
		require.Equal(t, http.StatusUnauthorized, res.Code)
	})

	t.Run("valid key with the scope", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v1/voices", nil)
		req.Header.Set("Authorization", "Bearer "+goodSecret)

		res := do(t, handler, req)

		require.Equal(t, http.StatusOK, res.Code)
		require.Equal(t, "59", res.Header().Get(gatewayhttp.HeaderRateLimitRemain),
			"a served request reports its remaining budget")
	})

	t.Run("valid key without the scope", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v1/usage", nil)
		req.Header.Set("Authorization", "Bearer "+goodSecret)

		res := do(t, handler, req)

		require.Equal(t, http.StatusForbidden, res.Code)
		require.Equal(t, gatewayhttp.CodeForbiddenScope, decodeProblem(t, res).Code)
	})

	t.Run("rate limited", func(t *testing.T) {
		limiter.decision = ratelimit.RateDecision{Limit: 60, RetryAfter: 2 * time.Second}
		req := httptest.NewRequest(http.MethodGet, "/v1/voices", nil)
		req.Header.Set("Authorization", "Bearer "+goodSecret)

		res := do(t, handler, req)

		require.Equal(t, http.StatusTooManyRequests, res.Code)
		require.Equal(t, "2", res.Header().Get(gatewayhttp.HeaderRetryAfter))
	})

	require.Equal(t, 1, reached, "only the authorised, unthrottled request reached a handler")
}
