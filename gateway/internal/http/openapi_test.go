package http_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	gatewayhttp "github.com/pcaokhai/vitts/gateway/internal/http"
)

func TestOpenAPIIsServedAsJSON(t *testing.T) {
	t.Parallel()

	handler, _, _ := newRouter(t)

	res := do(t, handler, httptest.NewRequest(http.MethodGet, "/openapi.json", nil))

	require.Equal(t, http.StatusOK, res.Code)
	require.Equal(t, "application/json", res.Header().Get("Content-Type"))

	var document map[string]any
	require.NoError(t, json.Unmarshal(res.Body.Bytes(), &document))
	require.Equal(t, "3.1.0", document["openapi"])

	paths, ok := document["paths"].(map[string]any)
	require.True(t, ok)
	for _, path := range []string{"/v1/synthesize", "/v1/synthesize/stream", "/v1/voices"} {
		require.Contains(t, paths, path, "the served contract describes the routes we serve")
	}
}

func TestOpenAPIIsServedAsYAML(t *testing.T) {
	t.Parallel()

	handler, _, _ := newRouter(t)

	res := do(t, handler, httptest.NewRequest(http.MethodGet, "/openapi.yaml", nil))

	require.Equal(t, http.StatusOK, res.Code)
	require.Equal(t, "application/yaml", res.Header().Get("Content-Type"))
	require.Contains(t, res.Body.String(), "openapi: 3.1.0")
}

// The contract is public: a client must be able to read it before it has a key.
func TestOpenAPINeedsNoCredential(t *testing.T) {
	t.Parallel()

	handler, _, _ := newRouter(t)

	req := httptest.NewRequest(http.MethodGet, "/openapi.json", nil)
	require.Empty(t, req.Header.Get("Authorization"))

	require.Equal(t, http.StatusOK, do(t, handler, req).Code)
}

// Every code the gateway can emit must exist in the contract's enum, or a client cannot
// handle it programmatically.
func TestEveryProblemCodeIsInTheContract(t *testing.T) {
	t.Parallel()

	handler, _, _ := newRouter(t)
	res := do(t, handler, httptest.NewRequest(http.MethodGet, "/openapi.json", nil))

	var document map[string]any
	require.NoError(t, json.Unmarshal(res.Body.Bytes(), &document))

	components := document["components"].(map[string]any)
	schemas := components["schemas"].(map[string]any)
	problem := schemas["Problem"].(map[string]any)
	properties := problem["properties"].(map[string]any)
	code := properties["code"].(map[string]any)

	declared := map[string]bool{}
	for _, value := range code["enum"].([]any) {
		declared[value.(string)] = true
	}

	for _, emitted := range []gatewayhttp.Code{
		gatewayhttp.CodeInvalidRequest, gatewayhttp.CodeTextTooLong, gatewayhttp.CodeUnknownVoice,
		gatewayhttp.CodeUnauthorized, gatewayhttp.CodeForbiddenScope, gatewayhttp.CodeQuotaExceeded,
		gatewayhttp.CodeRateLimited, gatewayhttp.CodeConcurrencyLimited, gatewayhttp.CodeOverloaded,
		gatewayhttp.CodeJobNotFound, gatewayhttp.CodeIdempotencyConflict, gatewayhttp.CodeInternal,
	} {
		require.True(t, declared[string(emitted)],
			"code %q is emitted by the gateway but missing from the OpenAPI enum", emitted)
	}
}
