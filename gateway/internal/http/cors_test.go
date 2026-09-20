package http_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/pcaokhai/vitts/gateway/internal/auth"
	gatewayhttp "github.com/pcaokhai/vitts/gateway/internal/http"
	"github.com/pcaokhai/vitts/gateway/internal/ratelimit"
)

const consoleOrigin = "https://console.vitts.dev"

func corsRouter(t *testing.T) http.Handler {
	t.Helper()

	repo := repoFunc(func(context.Context, []byte) (auth.Identity, error) {
		return liveIdentity(auth.ScopeUsage), nil
	})

	return gatewayhttp.Router(gatewayhttp.Deps{
		Logger:    zerolog.New(&strings.Builder{}),
		Readiness: gatewayhttp.NewReadiness(),
		Auth:      auth.NewAuthenticator(repo),
		Limiter:   &stubLimiter{decision: ratelimit.RateDecision{Allowed: true, Limit: 60}},
		Plans:     stubPlans{perMinute: 60},
		TenantAPI: []gatewayhttp.Route{{
			Method: http.MethodGet, Pattern: "/usage", Scope: auth.ScopeUsage,
			Handler: func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) },
		}},
		ConsoleOrigins: []string{consoleOrigin},
	})
}

// T-111. The console is allowed across origins; nothing else is, and the allowance never
// reaches the admin surface (ADR-012).
func TestCORSAllowsOnlyTheConsoleOrigin(t *testing.T) {
	t.Parallel()

	handler := corsRouter(t)

	t.Run("preflight from the console is answered", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodOptions, "/v1/usage", nil)
		req.Header.Set(gatewayhttp.HeaderOrigin, consoleOrigin)
		req.Header.Set(gatewayhttp.HeaderACRequestMethod, http.MethodGet)

		res := do(t, handler, req)

		require.Equal(t, http.StatusNoContent, res.Code)
		require.Equal(t, consoleOrigin, res.Header().Get(gatewayhttp.HeaderACAllowOrigin))
		require.Contains(t, res.Header().Get(gatewayhttp.HeaderACAllowHeaders), "Authorization")
		require.Contains(t, res.Header().Get(gatewayhttp.HeaderACExposeHeaders), "X-RateLimit-Remaining")
	})

	t.Run("another origin gets no allowance", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodOptions, "/v1/usage", nil)
		req.Header.Set(gatewayhttp.HeaderOrigin, "https://evil.example")
		req.Header.Set(gatewayhttp.HeaderACRequestMethod, http.MethodGet)

		res := do(t, handler, req)

		require.Equal(t, http.StatusForbidden, res.Code)
		require.Empty(t, res.Header().Get(gatewayhttp.HeaderACAllowOrigin))
	})

	t.Run("the response always varies on origin", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		req.Header.Set(gatewayhttp.HeaderOrigin, "https://evil.example")

		res := do(t, handler, req)

		require.Empty(t, res.Header().Get(gatewayhttp.HeaderACAllowOrigin))
	})

	t.Run("the admin surface is never cross-origin", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodOptions, "/admin/v1/tenants", nil)

		req.Header.Set(gatewayhttp.HeaderOrigin, consoleOrigin)
		req.Header.Set(gatewayhttp.HeaderACRequestMethod, http.MethodPost)

		res := do(t, handler, req)

		require.Empty(t, res.Header().Get(gatewayhttp.HeaderACAllowOrigin),
			"the console origin must not be allowed to reach admin endpoints")
	})
}
