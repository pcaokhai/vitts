package synth

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/pcaokhai/vitts/gateway/internal/ratelimit"
)

// LeaseAdapter lets the limiter satisfy this package's Leases port.
type LeaseAdapter struct {
	limiter *ratelimit.Limiter
}

// NewLeaseAdapter wires the adapter.
func NewLeaseAdapter(limiter *ratelimit.Limiter) *LeaseAdapter {
	return &LeaseAdapter{limiter: limiter}
}

// Acquire takes a concurrency slot for the tenant.
func (a *LeaseAdapter) Acquire(ctx context.Context, tenantID uuid.UUID, limit int32) (Lease, bool, error) {
	lease, ok, err := a.limiter.Acquire(ctx, tenantID, limit)
	if err != nil {
		return nil, false, fmt.Errorf("acquire lease: %w", err)
	}
	if !ok {
		return nil, false, nil
	}
	return &heldLease{limiter: a.limiter, lease: lease}, true, nil
}

type heldLease struct {
	limiter *ratelimit.Limiter
	lease   ratelimit.Lease
}

func (h *heldLease) Renew(ctx context.Context) error {
	if err := h.limiter.Renew(ctx, h.lease); err != nil {
		return fmt.Errorf("renew lease: %w", err)
	}
	return nil
}

// Release always runs, including on a cancelled request, so capacity is returned
// immediately rather than after the TTL.
func (h *heldLease) Release(ctx context.Context) {
	_ = h.limiter.Release(ctx, h.lease)
}
