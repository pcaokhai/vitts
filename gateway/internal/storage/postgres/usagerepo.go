package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/pcaokhai/vitts/gateway/internal/quota"
	"github.com/pcaokhai/vitts/gateway/internal/storage/postgres/db"
)

// UsageRepository reads authoritative usage totals. It implements quota.UsageSource.
type UsageRepository struct {
	pool *Pool
}

// NewUsageRepository wires the repository to a pool.
func NewUsageRepository(pool *Pool) *UsageRepository {
	return &UsageRepository{pool: pool}
}

// SumCharsSince totals billable characters per tenant in a time window.
//
// Cancelled streams count: ADR-010 bills the audio actually delivered, so their
// characters are part of the allowance the tenant has used.
func (r *UsageRepository) SumCharsSince(ctx context.Context, from, to time.Time) ([]quota.MonthlyUsage, error) {
	rows, err := r.pool.Queries.SumCharsByTenantSince(ctx, db.SumCharsByTenantSinceParams{
		CreatedAt:   pgtype.Timestamptz{Time: from, Valid: true},
		CreatedAt_2: pgtype.Timestamptz{Time: to, Valid: true},
	})
	if err != nil {
		return nil, fmt.Errorf("sum chars: %w", err)
	}

	usage := make([]quota.MonthlyUsage, 0, len(rows))
	for _, row := range rows {
		usage = append(usage, quota.MonthlyUsage{TenantID: row.TenantID, Chars: row.Chars})
	}
	return usage, nil
}
