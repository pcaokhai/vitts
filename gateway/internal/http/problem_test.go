package http_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	gatewayhttp "github.com/pcaokhai/vitts/gateway/internal/http"
)

func decodeProblem(t *testing.T, res *httptest.ResponseRecorder) gatewayhttp.Problem {
	t.Helper()

	var problem gatewayhttp.Problem
	require.NoError(t, json.NewDecoder(res.Body).Decode(&problem))
	return problem
}

func TestWriteProblemStatusPerCode(t *testing.T) {
	t.Parallel()

	tests := map[gatewayhttp.Code]int{
		gatewayhttp.CodeInvalidRequest:      http.StatusBadRequest,
		gatewayhttp.CodeTextTooLong:         http.StatusRequestEntityTooLarge,
		gatewayhttp.CodeUnknownVoice:        http.StatusUnprocessableEntity,
		gatewayhttp.CodeUnauthorized:        http.StatusUnauthorized,
		gatewayhttp.CodeForbiddenScope:      http.StatusForbidden,
		gatewayhttp.CodeJobNotFound:         http.StatusNotFound,
		gatewayhttp.CodeIdempotencyConflict: http.StatusConflict,
		gatewayhttp.CodeQuotaExceeded:       http.StatusPaymentRequired,
		gatewayhttp.CodeRateLimited:         http.StatusTooManyRequests,
		gatewayhttp.CodeConcurrencyLimited:  http.StatusTooManyRequests,
		gatewayhttp.CodeOverloaded:          http.StatusServiceUnavailable,
		gatewayhttp.CodeInternal:            http.StatusInternalServerError,
	}

	for code, wantStatus := range tests {
		t.Run(string(code), func(t *testing.T) {
			t.Parallel()

			res := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/v1/synthesize", nil)

			gatewayhttp.WriteProblem(res, req, gatewayhttp.NewError(code, "detail here"))

			require.Equal(t, wantStatus, res.Code)
			require.Equal(t, gatewayhttp.ContentTypeProblem, res.Header().Get("Content-Type"))

			problem := decodeProblem(t, res)
			require.Equal(t, code, problem.Code)
			require.Equal(t, wantStatus, problem.Status)
			require.NotEmpty(t, problem.Title)
			require.Equal(t, "/v1/synthesize", problem.Instance)
		})
	}
}

func TestWriteProblemHidesUnmappedErrors(t *testing.T) {
	t.Parallel()

	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/voices", nil)

	gatewayhttp.WriteProblem(res, req, errors.New("connection to 10.0.0.4 refused: bad password"))

	require.Equal(t, http.StatusInternalServerError, res.Code)
	problem := decodeProblem(t, res)
	require.Equal(t, gatewayhttp.CodeInternal, problem.Code)
	require.Empty(t, problem.Detail, "an unmapped error's message must not reach the caller")
	require.NotContains(t, res.Body.String(), "bad password")
}

func TestWriteProblemUnwrapsWrappedError(t *testing.T) {
	t.Parallel()

	wrapped := fmt.Errorf("synth use case: %w", gatewayhttp.NewError(gatewayhttp.CodeTextTooLong, "3000 max"))

	res := httptest.NewRecorder()
	gatewayhttp.WriteProblem(res, httptest.NewRequest(http.MethodPost, "/v1/synthesize", nil), wrapped)

	require.Equal(t, http.StatusRequestEntityTooLarge, res.Code)
	problem := decodeProblem(t, res)
	require.Equal(t, gatewayhttp.CodeTextTooLong, problem.Code)
	require.Equal(t, "3000 max", problem.Detail)
}

func TestErrorUnwrapsCause(t *testing.T) {
	t.Parallel()

	cause := errors.New("boom")
	err := &gatewayhttp.Error{Code: gatewayhttp.CodeInternal, Cause: cause}

	require.ErrorIs(t, err, cause)
	require.Contains(t, err.Error(), "boom")
}
