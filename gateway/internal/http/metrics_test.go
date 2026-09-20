package http_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	gatewayhttp "github.com/pcaokhai/vitts/gateway/internal/http"
	"github.com/pcaokhai/vitts/gateway/internal/telemetry"
)

func meteredRouter(t *testing.T) (http.Handler, *telemetry.Metrics) {
	t.Helper()

	metrics := telemetry.NewMetrics()
	handler := gatewayhttp.Router(gatewayhttp.Deps{
		Logger:    zerolog.New(&strings.Builder{}),
		Readiness: gatewayhttp.NewReadiness(),
		Metrics:   metrics,
	})
	return handler, metrics
}

func TestMetricsEndpointExposesRequestCounts(t *testing.T) {
	t.Parallel()

	handler, _ := meteredRouter(t)
	do(t, handler, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	res := do(t, handler, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	require.Equal(t, http.StatusOK, res.Code)
	body := res.Body.String()
	require.Contains(t, body, "http_requests_total")
	require.Contains(t, body, `route="/healthz"`)
}

// A 404 flood must not become a flood of label values.
func TestUnmatchedRoutesShareOneLabel(t *testing.T) {
	t.Parallel()

	handler, _ := meteredRouter(t)
	for _, path := range []string{"/nope/1", "/nope/2", "/nope/3"} {
		do(t, handler, httptest.NewRequest(http.MethodGet, path, nil))
	}

	body := do(t, handler, httptest.NewRequest(http.MethodGet, "/metrics", nil)).Body.String()

	require.Contains(t, body, `route="unmatched"`)
	require.NotContains(t, body, `route="/nope/1"`)
}

func TestMetricsEndpointNeedsNoCredential(t *testing.T) {
	t.Parallel()

	handler, _ := meteredRouter(t)
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	require.Empty(t, req.Header.Get("Authorization"))

	require.Equal(t, http.StatusOK, do(t, handler, req).Code)
}

func TestARouterWithoutMetricsStillServes(t *testing.T) {
	t.Parallel()

	handler, _, _ := newRouter(t)

	require.Equal(t, http.StatusOK, do(t, handler, httptest.NewRequest(http.MethodGet, "/healthz", nil)).Code)
	require.Equal(t, http.StatusNotFound, do(t, handler, httptest.NewRequest(http.MethodGet, "/metrics", nil)).Code)
}
