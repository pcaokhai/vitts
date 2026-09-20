package http

import (
	"context"
	"errors"
	"net/http"

	"github.com/rs/zerolog"

	"github.com/pcaokhai/vitts/gateway/internal/auth"
)

// HeaderAuthorization carries the tenant API key.
const HeaderAuthorization = "Authorization"

type identityKey struct{}

// IdentityFrom returns the authenticated caller, if the request passed Authenticate.
func IdentityFrom(ctx context.Context) (auth.Identity, bool) {
	identity, ok := ctx.Value(identityKey{}).(auth.Identity)
	return identity, ok
}

// Authenticate resolves the bearer key and attaches the identity to the request.
//
// Tenant id, key id and plan join the request logger so every later line is attributable;
// the secret and the full hash never do (ADR-008, US-05 acceptance criterion 4).
func Authenticate(authenticator *auth.Authenticator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			secret, err := auth.ParseBearer(r.Header.Get(HeaderAuthorization))
			if err != nil {
				WriteProblem(w, r, NewError(CodeUnauthorized, "missing or malformed API key"))
				return
			}

			identity, err := authenticator.Authenticate(r.Context(), secret)
			if err != nil {
				// A lookup failure is an outage, not a rejection: reporting it as 401
				// would send tenants chasing their credentials during our incident.
				if errors.Is(err, auth.ErrLookupFailure) {
					WriteProblem(w, r, &Error{Code: CodeInternal, Cause: err})
					return
				}
				WriteProblem(w, r, NewError(CodeUnauthorized, "invalid API key"))
				return
			}

			logger := zerolog.Ctx(r.Context()).With().
				Str("tenant_id", identity.TenantID.String()).
				Str("key_id", identity.KeyID.String()).
				Str("plan_id", identity.PlanID).
				Logger()

			ctx := context.WithValue(logger.WithContext(r.Context()), identityKey{}, identity)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RequireScope rejects a caller whose key does not carry the scope.
//
// It must run after Authenticate; an unauthenticated request here is a wiring bug, and
// failing closed with 401 is the safe answer to it.
func RequireScope(scope auth.Scope) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			identity, ok := IdentityFrom(r.Context())
			if !ok {
				WriteProblem(w, r, NewError(CodeUnauthorized, "missing or malformed API key"))
				return
			}
			if err := identity.Require(scope); err != nil {
				WriteProblem(w, r, NewError(CodeForbiddenScope, "key lacks scope "+string(scope)))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// OptionalAuthenticate attaches an identity when the request carries a usable key and
// lets the request through when it does not.
//
// It exists for US-13: the voice catalogue is readable without a key, and a key narrows
// the listing to the caller's own voices. A malformed or invalid key is still passed
// through as anonymous rather than rejected — the endpoint is public, so a bad key can
// only ever cost the caller the tenant-scoped rows they were hoping to see, and failing
// the request instead would make a public endpoint behave like a private one.
//
// A lookup outage is the exception: it is reported, because silently serving the public
// subset would hide the outage and change what a keyed caller sees.
func OptionalAuthenticate(authenticator *auth.Authenticator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			secret, err := auth.ParseBearer(r.Header.Get(HeaderAuthorization))
			if err != nil {
				next.ServeHTTP(w, r)
				return
			}

			identity, err := authenticator.Authenticate(r.Context(), secret)
			if err != nil {
				if errors.Is(err, auth.ErrLookupFailure) {
					WriteProblem(w, r, &Error{Code: CodeInternal, Cause: err})
					return
				}
				next.ServeHTTP(w, r)
				return
			}

			logger := zerolog.Ctx(r.Context()).With().
				Str("tenant_id", identity.TenantID.String()).
				Str("key_id", identity.KeyID.String()).
				Str("plan_id", identity.PlanID).
				Logger()

			ctx := context.WithValue(logger.WithContext(r.Context()), identityKey{}, identity)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
