package http_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/pcaokhai/vitts/gateway/internal/auth"
	gatewayhttp "github.com/pcaokhai/vitts/gateway/internal/http"
)

const goodSecret = "zt_live_goodsecret"

// repoFunc adapts a function to auth.Repository.
type repoFunc func(ctx context.Context, hash []byte) (auth.Identity, error)

func (f repoFunc) FindByHash(ctx context.Context, hash []byte) (auth.Identity, error) {
	return f(ctx, hash)
}

func liveIdentity(scopes ...auth.Scope) auth.Identity {
	return auth.Identity{
		TenantID:       uuid.New(),
		KeyID:          uuid.New(),
		PlanID:         "dev",
		Scopes:         scopes,
		KeyFingerprint: "aabbccddeeff",
	}
}

// protected builds a handler chained exactly as the router does, so the test exercises
// the real middleware order.
func protected(repo auth.Repository, scope *auth.Scope, logs *strings.Builder) http.Handler {
	var final http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity, ok := gatewayhttp.IdentityFrom(r.Context())
		if !ok {
			http.Error(w, "no identity", http.StatusTeapot)
			return
		}
		zerolog.Ctx(r.Context()).Info().Msg("handler reached")
		_, _ = w.Write([]byte(identity.TenantID.String()))
	})

	if scope != nil {
		final = gatewayhttp.RequireScope(*scope)(final)
	}
	handler := gatewayhttp.Authenticate(auth.NewAuthenticator(repo))(final)
	return gatewayhttp.RequestID(gatewayhttp.Logger(zerolog.New(logs))(handler))
}

func TestAuthenticateAcceptsALiveKey(t *testing.T) {
	t.Parallel()

	identity := liveIdentity(auth.ScopeSynth)
	logs := &strings.Builder{}
	handler := protected(repoFunc(func(_ context.Context, hash []byte) (auth.Identity, error) {
		require.Equal(t, auth.Hash(goodSecret), hash)
		return identity, nil
	}), nil, logs)

	req := httptest.NewRequest(http.MethodGet, "/v1/voices", nil)
	req.Header.Set("Authorization", "Bearer "+goodSecret)
	res := do(t, handler, req)

	require.Equal(t, http.StatusOK, res.Code)
	require.Equal(t, identity.TenantID.String(), res.Body.String())
	require.Contains(t, logs.String(), identity.TenantID.String(), "logs are attributable")
	require.NotContains(t, logs.String(), goodSecret, "the secret must never be logged")
}

func TestAuthenticateRejectsEveryBadCredentialTheSameWay(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		header string
		repo   auth.Repository
	}{
		"missing header": {header: ""},
		"not bearer":     {header: "Basic dXNlcjpwYXNz"},
		"foreign prefix": {header: "Bearer sk_live_abc"},
		"revoked or unknown key": {
			header: "Bearer " + goodSecret,
			repo: repoFunc(func(context.Context, []byte) (auth.Identity, error) {
				return auth.Identity{}, auth.ErrUnauthorized
			}),
		},
		"suspended tenant": {
			header: "Bearer " + goodSecret,
			repo: repoFunc(func(context.Context, []byte) (auth.Identity, error) {
				return auth.Identity{}, auth.ErrUnauthorized
			}),
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			repo := tc.repo
			if repo == nil {
				repo = repoFunc(func(context.Context, []byte) (auth.Identity, error) {
					return auth.Identity{}, auth.ErrUnauthorized
				})
			}
			req := httptest.NewRequest(http.MethodGet, "/v1/voices", nil)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}

			res := do(t, protected(repo, nil, &strings.Builder{}), req)

			require.Equal(t, http.StatusUnauthorized, res.Code)
			problem := decodeProblem(t, res)
			require.Equal(t, gatewayhttp.CodeUnauthorized, problem.Code)
		})
	}
}

func TestAuthenticateReportsALookupOutageAsInternal(t *testing.T) {
	t.Parallel()

	repo := repoFunc(func(context.Context, []byte) (auth.Identity, error) {
		return auth.Identity{}, errors.Join(auth.ErrLookupFailure, errors.New("dial tcp: refused"))
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/voices", nil)
	req.Header.Set("Authorization", "Bearer "+goodSecret)
	res := do(t, protected(repo, nil, &strings.Builder{}), req)

	require.Equal(t, http.StatusInternalServerError, res.Code,
		"our outage must not look like the tenant's bad credential")
	require.NotContains(t, res.Body.String(), "dial tcp")
}

func TestRequireScope(t *testing.T) {
	t.Parallel()

	scope := auth.ScopeJobs
	tests := map[string]struct {
		granted []auth.Scope
		want    int
	}{
		"scope present": {granted: []auth.Scope{auth.ScopeSynth, auth.ScopeJobs}, want: http.StatusOK},
		"scope missing": {granted: []auth.Scope{auth.ScopeSynth}, want: http.StatusForbidden},
		"no scopes":     {granted: nil, want: http.StatusForbidden},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			repo := repoFunc(func(context.Context, []byte) (auth.Identity, error) {
				return liveIdentity(tc.granted...), nil
			})
			req := httptest.NewRequest(http.MethodPost, "/v1/jobs", nil)
			req.Header.Set("Authorization", "Bearer "+goodSecret)

			res := do(t, protected(repo, &scope, &strings.Builder{}), req)

			require.Equal(t, tc.want, res.Code)
			if tc.want == http.StatusForbidden {
				require.Equal(t, gatewayhttp.CodeForbiddenScope, decodeProblem(t, res).Code)
			}
		})
	}
}

func TestRequireScopeFailsClosedWithoutAuthentication(t *testing.T) {
	t.Parallel()

	handler := gatewayhttp.RequestID(gatewayhttp.RequireScope(auth.ScopeSynth)(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		})))

	res := do(t, handler, httptest.NewRequest(http.MethodGet, "/v1/voices", nil))

	require.Equal(t, http.StatusUnauthorized, res.Code, "a wiring bug must not grant access")
}

func TestAdminGuard(t *testing.T) {
	t.Parallel()

	// Built rather than written out: a 32-character literal here is indistinguishable
	// from a real leaked key to a secret scanner, and to a reviewer.
	adminKey := strings.Repeat("ab", 16)
	allowlist := []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")}

	tests := map[string]struct {
		key    string
		peer   string
		want   int
		header string
	}{
		"correct key from allowed network": {key: adminKey, peer: "192.0.2.10:1234", want: http.StatusOK},
		"correct key from elsewhere":       {key: adminKey, peer: "198.51.100.7:1234", want: http.StatusUnauthorized},
		"wrong key from allowed network":   {key: "wrong", peer: "192.0.2.10:1234", want: http.StatusUnauthorized},
		"no key at all":                    {key: "", peer: "192.0.2.10:1234", want: http.StatusUnauthorized},
		"forwarded header cannot spoof": {
			key: adminKey, peer: "198.51.100.7:1234", want: http.StatusUnauthorized,
			header: "192.0.2.10",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			guard := gatewayhttp.NewAdminGuard(adminKey, allowlist)
			handler := gatewayhttp.RequestID(guard.Middleware(
				http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(http.StatusOK)
				})))

			req := httptest.NewRequest(http.MethodPost, "/admin/v1/tenants", nil)
			req.RemoteAddr = tc.peer
			if tc.key != "" {
				req.Header.Set(gatewayhttp.HeaderAdminKey, tc.key)
			}
			if tc.header != "" {
				req.Header.Set("X-Forwarded-For", tc.header)
			}

			require.Equal(t, tc.want, do(t, handler, req).Code)
		})
	}
}

func TestAdminGuardWithEmptyAllowlistDeniesEverything(t *testing.T) {
	t.Parallel()

	guard := gatewayhttp.NewAdminGuard(strings.Repeat("ab", 16), nil)
	handler := gatewayhttp.RequestID(guard.Middleware(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })))

	req := httptest.NewRequest(http.MethodPost, "/admin/v1/tenants", nil)
	req.RemoteAddr = "192.0.2.10:1234"
	req.Header.Set(gatewayhttp.HeaderAdminKey, strings.Repeat("ab", 16))

	require.Equal(t, http.StatusUnauthorized, do(t, handler, req).Code)
}
