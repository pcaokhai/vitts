package jobs

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

// dnsTimeout bounds resolution so a hostile hostname cannot stall job creation.
const dnsTimeout = 3 * time.Second

// SSRFGuard rejects webhook URLs that point back into infrastructure.
//
// It resolves the hostname and inspects every address, because a name that looks public
// can answer with 127.0.0.1. The orchestrator re-checks before each delivery, since DNS
// can change between job creation and completion (US-15 acceptance criterion 3, FL-03).
type SSRFGuard struct {
	resolve func(ctx context.Context, host string) ([]netip.Addr, error)
}

// NewSSRFGuard wires the guard to the system resolver.
func NewSSRFGuard() *SSRFGuard {
	return &SSRFGuard{resolve: resolveSystem}
}

// NewSSRFGuardWithResolver injects a resolver, so the range checks can be tested without
// depending on what public DNS happens to answer today.
func NewSSRFGuardWithResolver(
	resolve func(ctx context.Context, host string) ([]netip.Addr, error),
) *SSRFGuard {
	return &SSRFGuard{resolve: resolve}
}

// Check validates a webhook URL.
func (g *SSRFGuard) Check(ctx context.Context, rawURL string) error {
	if rawURL == "" {
		return nil // no webhook requested
	}

	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("%w: not a URL", ErrWebhookRejected)
	}

	// HTTPS only: a signed payload sent in the clear is still readable, and the
	// signature does not make the body private (docs/07-permissions.md).
	if !strings.EqualFold(parsed.Scheme, "https") {
		return fmt.Errorf("%w: only https is accepted", ErrWebhookRejected)
	}

	host := parsed.Hostname()
	if host == "" {
		return fmt.Errorf("%w: no host", ErrWebhookRejected)
	}

	// A literal address skips DNS but not the range check.
	if addr, parseErr := netip.ParseAddr(host); parseErr == nil {
		return g.checkAddr(addr)
	}

	ctx, cancel := context.WithTimeout(ctx, dnsTimeout)
	defer cancel()

	addrs, err := g.resolve(ctx, host)
	if err != nil {
		return fmt.Errorf("%w: host does not resolve", ErrWebhookRejected)
	}
	if len(addrs) == 0 {
		return fmt.Errorf("%w: host resolves to nothing", ErrWebhookRejected)
	}

	// Every address must pass: a name answering with one public and one private address
	// would otherwise be a way in.
	for _, addr := range addrs {
		if err := g.checkAddr(addr); err != nil {
			return err
		}
	}
	return nil
}

func (g *SSRFGuard) checkAddr(addr netip.Addr) error {
	addr = addr.Unmap()

	switch {
	case addr.IsLoopback():
		return fmt.Errorf("%w: loopback address", ErrWebhookRejected)
	case addr.IsPrivate():
		return fmt.Errorf("%w: private address", ErrWebhookRejected)
	case addr.IsLinkLocalUnicast(), addr.IsLinkLocalMulticast():
		// 169.254.169.254 is the cloud metadata endpoint; this is the range that makes
		// SSRF a credential-theft bug rather than a nuisance.
		return fmt.Errorf("%w: link-local address", ErrWebhookRejected)
	case addr.IsMulticast():
		return fmt.Errorf("%w: multicast address", ErrWebhookRejected)
	case addr.IsUnspecified():
		return fmt.Errorf("%w: unspecified address", ErrWebhookRejected)
	case !addr.IsValid():
		return fmt.Errorf("%w: invalid address", ErrWebhookRejected)
	}

	// Carrier-grade NAT and IPv6 unique-local are private in practice but are not
	// covered by IsPrivate.
	if addr.Is4() {
		if cgnat := netip.MustParsePrefix("100.64.0.0/10"); cgnat.Contains(addr) {
			return fmt.Errorf("%w: carrier-grade NAT address", ErrWebhookRejected)
		}
	}
	if addr.Is6() {
		if ula := netip.MustParsePrefix("fc00::/7"); ula.Contains(addr) {
			return fmt.Errorf("%w: unique-local address", ErrWebhookRejected)
		}
	}

	return nil
}

func resolveSystem(ctx context.Context, host string) ([]netip.Addr, error) {
	addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, fmt.Errorf("resolve %s: %w", host, err)
	}
	return addrs, nil
}
