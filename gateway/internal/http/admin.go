package http

import (
	"crypto/sha256"
	"crypto/subtle"
	"net"
	"net/http"
	"net/netip"

	"github.com/rs/zerolog"
)

// HeaderAdminKey carries the operator credential (docs/07-permissions.md).
const HeaderAdminKey = "X-Admin-Key"

// AdminGuard authorises admin endpoints: a constant-time key comparison plus an IP
// allowlist. Both must pass; the key alone is not enough for endpoints that can create
// tenants and read across them.
type AdminGuard struct {
	keyDigest [sha256.Size]byte
	allowed   []netip.Prefix
}

// NewAdminGuard builds the guard from configuration. An empty allowlist denies
// everything: a misconfigured allowlist must close the door, not open it.
func NewAdminGuard(key string, allowed []netip.Prefix) *AdminGuard {
	return &AdminGuard{keyDigest: sha256.Sum256([]byte(key)), allowed: allowed}
}

// Middleware rejects any request that is not a permitted operator.
//
// Failures return 401 with no hint about which check failed, and the peer address is
// logged so a probe is visible in operations.
func (g *AdminGuard) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		peer, peerOK := peerAddr(r)
		presented := sha256.Sum256([]byte(r.Header.Get(HeaderAdminKey)))
		keyOK := subtle.ConstantTimeCompare(presented[:], g.keyDigest[:]) == 1

		if !keyOK || !peerOK || !g.permits(peer) {
			zerolog.Ctx(r.Context()).Warn().
				Str("peer", peer.String()).
				Bool("key_ok", keyOK).
				Msg("admin request denied")
			WriteProblem(w, r, NewError(CodeUnauthorized, "admin credentials required"))
			return
		}

		next.ServeHTTP(w, r)
	})
}

func (g *AdminGuard) permits(addr netip.Addr) bool {
	for _, prefix := range g.allowed {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

// peerAddr is the transport peer, never a forwarded header: X-Forwarded-For is
// caller-controlled and would make the allowlist trivially bypassable. A proxy in front
// of the gateway must therefore be inside the allowlist itself.
func peerAddr(r *http.Request) (netip.Addr, bool) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, false
	}
	return addr.Unmap(), true
}
