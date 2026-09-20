package http

import (
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

// CORS headers the console needs.
const (
	HeaderOrigin          = "Origin"
	HeaderACAllowOrigin   = "Access-Control-Allow-Origin"
	HeaderACAllowMethods  = "Access-Control-Allow-Methods"
	HeaderACAllowHeaders  = "Access-Control-Allow-Headers"
	HeaderACExposeHeaders = "Access-Control-Expose-Headers"
	HeaderACMaxAge        = "Access-Control-Max-Age"
	HeaderACRequestMethod = "Access-Control-Request-Method"
	HeaderVary            = "Vary"
)

// corsMaxAge is how long a browser may cache a preflight. A day is the usual ceiling
// browsers honour, and the allowance never changes between deploys.
const corsMaxAge = 24 * time.Hour

// corsMethods and corsHeaders are what the console actually uses: reading usage, creating
// and revoking keys. Nothing wider, so a future endpoint has to be considered rather than
// inherited.
var (
	corsMethods = []string{http.MethodGet, http.MethodPost, http.MethodDelete, http.MethodOptions}
	corsHeaders = []string{HeaderAuthorization, "Content-Type", "Idempotency-Key"}
	// corsExposed lets the console read the budget headers it shows the tenant. Without
	// this a browser hides them even on a request it was allowed to make.
	corsExposed = []string{
		HeaderRateLimitLimit, HeaderRateLimitRemain, HeaderRateLimitResetSec, HeaderRetryAfter,
	}
)

// CORS allows exactly the origins given, and only for the routes it wraps.
//
// It is applied to the tenant API alone. The admin surface stays same-origin: it is
// IP-allowlisted and holds the key that creates tenants, so no browser has business
// calling it cross-origin (ADR-012, docs/07-permissions.md).
//
// Credentials are deliberately not allowed. The console authenticates with a bearer key
// it holds in the tab, never a cookie, so there is no ambient authority for another site
// to ride on: a page that forges a request to this API still has to know the key.
func CORS(origins []string) func(http.Handler) http.Handler {
	allowed := make([]string, 0, len(origins))
	for _, origin := range origins {
		if trimmed := strings.TrimSpace(origin); trimmed != "" {
			allowed = append(allowed, trimmed)
		}
	}

	methods := strings.Join(corsMethods, ", ")
	headers := strings.Join(corsHeaders, ", ")
	exposed := strings.Join(corsExposed, ", ")

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get(HeaderOrigin)

			// Vary on Origin whether or not this one is allowed: a cache that served one
			// origin's response to another would hand out the allowance itself.
			w.Header().Add(HeaderVary, HeaderOrigin)

			if origin == "" || !slices.Contains(allowed, origin) {
				// Not an allowed origin: answer without CORS headers and let the browser
				// refuse. A preflight is still terminated here so it never reaches a
				// handler that would have to understand OPTIONS.
				if r.Method == http.MethodOptions && r.Header.Get(HeaderACRequestMethod) != "" {
					w.WriteHeader(http.StatusForbidden)
					return
				}
				next.ServeHTTP(w, r)
				return
			}

			w.Header().Set(HeaderACAllowOrigin, origin)
			w.Header().Set(HeaderACExposeHeaders, exposed)

			if r.Method == http.MethodOptions && r.Header.Get(HeaderACRequestMethod) != "" {
				w.Header().Set(HeaderACAllowMethods, methods)
				w.Header().Set(HeaderACAllowHeaders, headers)
				w.Header().Set(HeaderACMaxAge, strconv.Itoa(int(corsMaxAge.Seconds())))
				w.WriteHeader(http.StatusNoContent)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
