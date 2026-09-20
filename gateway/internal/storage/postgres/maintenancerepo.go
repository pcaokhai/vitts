package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/pcaokhai/vitts/gateway/internal/maintenance"
	"github.com/pcaokhai/vitts/gateway/internal/storage/postgres/db"
	"github.com/pcaokhai/vitts/gateway/internal/usage"
)

// MaintenanceRepository implements maintenance.Store.
type MaintenanceRepository struct {
	pool *Pool
}

// NewMaintenanceRepository wires the repository.
func NewMaintenanceRepository(pool *Pool) *MaintenanceRepository {
	return &MaintenanceRepository{pool: pool}
}

// RollupUsage recomputes usage_daily for a window.
func (r *MaintenanceRepository) RollupUsage(ctx context.Context, from, to time.Time) error {
	if err := r.pool.Queries.RollupUsageDaily(ctx, db.RollupUsageDailyParams{
		CreatedAt:   pgtype.Timestamptz{Time: from, Valid: true},
		CreatedAt_2: pgtype.Timestamptz{Time: to, Valid: true},
	}); err != nil {
		return fmt.Errorf("rollup usage: %w", err)
	}
	return nil
}

// EvictableCacheEntries lists entries idle since before the cutoff.
func (r *MaintenanceRepository) EvictableCacheEntries(ctx context.Context, idleBefore time.Time, limit int32) ([]maintenance.CacheEntry, error) {
	rows, err := r.pool.Queries.EvictableCacheEntries(ctx, db.EvictableCacheEntriesParams{
		LastHitAt: pgtype.Timestamptz{Time: idleBefore, Valid: true},
		Limit:     limit,
	})
	if err != nil {
		return nil, fmt.Errorf("list evictable entries: %w", err)
	}

	out := make([]maintenance.CacheEntry, 0, len(rows))
	for _, row := range rows {
		out = append(out, maintenance.CacheEntry{CacheKey: row.CacheKey, S3Key: row.S3Key})
	}
	return out, nil
}

// DeleteCacheEntry removes one catalogue row.
func (r *MaintenanceRepository) DeleteCacheEntry(ctx context.Context, cacheKey []byte) error {
	if err := r.pool.Queries.DeleteCacheEntry(ctx, cacheKey); err != nil {
		return fmt.Errorf("delete cache entry: %w", err)
	}
	return nil
}

// EnsurePartition creates the month's synth_requests partition if it is missing.
//
// It calls the function migration 0001 defines, so a partition has exactly one
// definition whether it is created at migration time or by this job.
func (r *MaintenanceRepository) EnsurePartition(ctx context.Context, at time.Time) (string, error) {
	var name string
	err := r.pool.pool.QueryRow(ctx,
		`select ensure_synth_requests_partition($1)`,
		pgtype.Timestamptz{Time: at, Valid: true},
	).Scan(&name)
	if err != nil {
		return "", fmt.Errorf("ensure partition: %w", err)
	}
	return name, nil
}

// ByDay reads the daily rollup for a tenant, implementing usage.Reader.
func (r *UsageRepository) ByDay(ctx context.Context, tenantID uuid.UUID, from, to time.Time) ([]usage.Day, error) {
	rows, err := r.pool.Queries.UsageByDay(ctx, db.UsageByDayParams{
		TenantID: tenantID,
		Day:      pgtype.Date{Time: from, Valid: true},
		Day_2:    pgtype.Date{Time: to, Valid: true},
	})
	if err != nil {
		return nil, fmt.Errorf("read usage by day: %w", err)
	}

	out := make([]usage.Day, 0, len(rows))
	for _, row := range rows {
		out = append(out, usage.Day{
			Day: row.Day.Time, Chars: row.Chars, AudioMS: row.AudioMs,
			Requests: row.Requests, CacheHits: row.CacheHits,
		})
	}
	return out, nil
}
