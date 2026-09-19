package synth

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/pcaokhai/vitts/gateway/internal/quota"
)

// QuotaAdapter lets the quota service satisfy this package's port without either side
// importing the other's types. It lives here because the port is defined here.
type QuotaAdapter struct {
	service *quota.Service
}

// NewQuotaAdapter wires the adapter.
func NewQuotaAdapter(service *quota.Service) *QuotaAdapter { return &QuotaAdapter{service: service} }

// Check reports whether the request fits in the allowance.
func (a *QuotaAdapter) Check(ctx context.Context, tenantID uuid.UUID, planLimit, chars int64) (Allowance, error) {
	decision, err := a.service.Check(ctx, tenantID, planLimit, chars)
	if err != nil {
		return Allowance{}, fmt.Errorf("quota check: %w", err)
	}
	return Allowance{Allowed: decision.Allowed, Used: decision.Used}, nil
}

// Record adds consumed characters.
func (a *QuotaAdapter) Record(ctx context.Context, tenantID uuid.UUID, chars int64) error {
	if err := a.service.Record(ctx, tenantID, chars); err != nil {
		return fmt.Errorf("quota record: %w", err)
	}
	return nil
}

// NopMeter discards usage records. It keeps the use case wired before task 1.12 lands
// the real meter, rather than leaving a nil that would panic on the first request.
type NopMeter struct{}

// Record does nothing.
func (NopMeter) Record(context.Context, Usage) {}
