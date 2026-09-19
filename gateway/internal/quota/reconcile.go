package quota

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

// ReconcileInterval is how often the counter is corrected from Postgres (FL-07).
const ReconcileInterval = time.Hour

// MonthlyUsage is one tenant's authoritative month-to-date total.
type MonthlyUsage struct {
	TenantID uuid.UUID
	Chars    int64
}

// UsageSource reads the authoritative totals. Implemented by the Postgres adapter.
type UsageSource interface {
	SumCharsSince(ctx context.Context, from, to time.Time) ([]MonthlyUsage, error)
}

// Reconciler corrects the Redis counter from Postgres.
//
// Redis is a cache of the truth, not the truth (docs/05-data-model.md § 4): a lost
// INCRBY, an evicted key or a flushed instance would otherwise let a tenant run past
// its allowance indefinitely.
type Reconciler struct {
	service *Service
	source  UsageSource
	logger  zerolog.Logger
}

// NewReconciler wires the job.
func NewReconciler(service *Service, source UsageSource, logger zerolog.Logger) *Reconciler {
	return &Reconciler{service: service, source: source, logger: logger}
}

// RunOnce recomputes every tenant's counter for the current month.
//
// Tenants with no requests this month are not touched: their counter is either absent or
// stale-but-zero, and rewriting every tenant's key every hour would be work proportional
// to the customer base rather than to activity.
func (r *Reconciler) RunOnce(ctx context.Context) error {
	now := r.service.now().UTC()
	from := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	to := from.AddDate(0, 1, 0)

	totals, err := r.source.SumCharsSince(ctx, from, to)
	if err != nil {
		return fmt.Errorf("read authoritative usage: %w", err)
	}

	var corrected int
	for _, total := range totals {
		before, err := r.service.Used(ctx, total.TenantID)
		if err != nil {
			return fmt.Errorf("read counter for %s: %w", total.TenantID, err)
		}
		if before == total.Chars {
			continue
		}

		if err := r.service.Set(ctx, total.TenantID, from, total.Chars); err != nil {
			return fmt.Errorf("correct counter for %s: %w", total.TenantID, err)
		}
		corrected++
		r.logger.Warn().
			Str("tenant_id", total.TenantID.String()).
			Int64("counter", before).
			Int64("authoritative", total.Chars).
			Msg("quota counter corrected")
	}

	r.logger.Info().Int("tenants", len(totals)).Int("corrected", corrected).
		Msg("quota reconcile complete")
	return nil
}

// Run reconciles on ReconcileInterval until ctx is cancelled. It runs once immediately,
// so a gateway that just started with a cold Redis does not wait an hour to be correct.
func (r *Reconciler) Run(ctx context.Context) {
	if err := r.RunOnce(ctx); err != nil && ctx.Err() == nil {
		r.logger.Error().Err(err).Msg("quota reconcile failed")
	}

	ticker := time.NewTicker(ReconcileInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := r.RunOnce(ctx); err != nil && ctx.Err() == nil {
				r.logger.Error().Err(err).Msg("quota reconcile failed")
			}
		}
	}
}
