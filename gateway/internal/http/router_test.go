package http_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	gatewayhttp "github.com/pcaokhai/vitts/gateway/internal/http"
)

func newRouter(t *testing.T) (http.Handler, *gatewayhttp.Readiness, *strings.Builder) {
	t.Helper()

	logs := &strings.Builder{}
	readiness := gatewayhttp.NewReadiness()
	return gatewayhttp.Router(zerolog.New(logs), readiness), readiness, logs
}

func do(t *testing.T, handler http.Handler, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()

	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	return res
}

func TestHealthzIsIndependentOfDependencies(t *testing.T) {
	t.Parallel()

	handler, readiness, _ := newRouter(t)
	readiness.Register("postgres", func(context.Context) error { return errors.New("down") })

	res := do(t, handler, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	require.Equal(t, http.StatusOK, res.Code, "liveness must not depend on a database")
}

func TestReadyzWithNoChecksIsReady(t *testing.T) {
	t.Parallel()

	handler, _, _ := newRouter(t)

	res := do(t, handler, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	require.Equal(t, http.StatusOK, res.Code)

	var status gatewayhttp.Status
	require.NoError(t, json.NewDecoder(res.Body).Decode(&status))
	require.True(t, status.Ready)
	require.Empty(t, status.Failing)
}

func TestReadyzReportsEachFailingDependency(t *testing.T) {
	t.Parallel()

	handler, readiness, _ := newRouter(t)
	readiness.Register("postgres", func(context.Context) error { return nil })
	readiness.Register("redis", func(context.Context) error { return errors.New("dial tcp: refused") })
	readiness.Register("workers", func(context.Context) error { return errors.New("no worker ready") })

	res := do(t, handler, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	require.Equal(t, http.StatusServiceUnavailable, res.Code)

	var status gatewayhttp.Status
	require.NoError(t, json.NewDecoder(res.Body).Decode(&status))
	require.False(t, status.Ready)
	require.Equal(t, []string{"redis", "workers"}, status.Failing)
	require.Equal(t, "ok", status.Checks["postgres"])
	require.NotContains(t, res.Body.String(), "refused", "a probe's error must not leak outward")
}

func TestUnknownRouteIsProblemJSON(t *testing.T) {
	t.Parallel()

	handler, _, _ := newRouter(t)

	res := do(t, handler, httptest.NewRequest(http.MethodGet, "/v1/nope", nil))

	require.Equal(t, http.StatusNotFound, res.Code)
	require.Equal(t, gatewayhttp.ContentTypeProblem, res.Header().Get("Content-Type"))

	problem := decodeProblem(t, res)
	require.Equal(t, gatewayhttp.CodeInvalidRequest, problem.Code)
	require.NotEmpty(t, problem.RequestID, "every error response carries the request id")
}

func TestMethodNotAllowedIsProblemJSON(t *testing.T) {
	t.Parallel()

	handler, _, _ := newRouter(t)

	res := do(t, handler, httptest.NewRequest(http.MethodPost, "/healthz", nil))

	require.Equal(t, http.StatusMethodNotAllowed, res.Code)
	require.Equal(t, gatewayhttp.ContentTypeProblem, res.Header().Get("Content-Type"))
}

func TestRequestIDIsGeneratedAndReturned(t *testing.T) {
	t.Parallel()

	handler, _, _ := newRouter(t)

	res := do(t, handler, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	require.NotEmpty(t, res.Header().Get(gatewayhttp.HeaderRequestID))
}

func TestRequestIDIsEchoedAndSanitised(t *testing.T) {
	t.Parallel()

	tests := map[string]struct{ sent, want string }{
		"clean id is kept":        {sent: "req-abc.123_X", want: "req-abc.123_X"},
		"log injection stripped":  {sent: "abc\ndef evil=1", want: "abcdefevil1"},
		"oversized id truncated":  {sent: strings.Repeat("a", 200), want: strings.Repeat("a", 64)},
		"unusable id regenerated": {sent: "!!!", want: ""},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			handler, _, _ := newRouter(t)
			req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
			req.Header.Set(gatewayhttp.HeaderRequestID, tc.sent)

			got := do(t, handler, req).Header().Get(gatewayhttp.HeaderRequestID)

			if tc.want == "" {
				require.NotEmpty(t, got)
				require.NotEqual(t, tc.sent, got)
				return
			}
			require.Equal(t, tc.want, got)
		})
	}
}

func TestAccessLogRecordsMetadataOnly(t *testing.T) {
	t.Parallel()

	handler, _, logs := newRouter(t)
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Header.Set("Authorization", "Bearer zt_live_supersecret")

	do(t, handler, req)

	line := logs.String()
	require.Contains(t, line, `"path":"/healthz"`)
	require.Contains(t, line, `"status":200`)
	require.NotContains(t, line, "zt_live_supersecret", "logs must never carry credentials")
}

func TestPanicBecomesProblemJSON(t *testing.T) {
	t.Parallel()

	logs := &strings.Builder{}
	handler := gatewayhttp.Recover(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("handler exploded")
	}))
	logged := gatewayhttp.RequestID(gatewayhttp.Logger(zerolog.New(logs))(handler))

	res := do(t, logged, httptest.NewRequest(http.MethodGet, "/v1/synthesize", nil))

	require.Equal(t, http.StatusInternalServerError, res.Code)
	problem := decodeProblem(t, res)
	require.Equal(t, gatewayhttp.CodeInternal, problem.Code)
	require.NotContains(t, res.Body.String(), "handler exploded")
	require.Contains(t, logs.String(), "panic recovered")
}

func TestReadyzBodyIsJSON(t *testing.T) {
	t.Parallel()

	handler, _, _ := newRouter(t)

	res := do(t, handler, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	body, err := io.ReadAll(res.Body)
	require.NoError(t, err)
	require.True(t, json.Valid(body))
	require.Equal(t, "application/json", res.Header().Get("Content-Type"))
}
