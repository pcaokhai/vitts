package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/pcaokhai/vitts/gateway/internal/storage/postgres/db"
	"github.com/pcaokhai/vitts/gateway/internal/usage"
)

// UsageWriter persists metering batches. It implements usage.Writer.
type UsageWriter struct {
	pool *Pool
}

// NewUsageWriter wires the writer.
func NewUsageWriter(pool *Pool) *UsageWriter { return &UsageWriter{pool: pool} }

// WriteBatch inserts a batch inside one transaction.
//
// Inserts are idempotent on (id, created_at), so the meter may retry a batch without
// double-billing (docs/05-data-model.md § 4). One statement per row is deliberate: the
// batch is already off the request path, and correctness beats a round trip here.
func (w *UsageWriter) WriteBatch(ctx context.Context, records []usage.Record) error {
	if len(records) == 0 {
		return nil
	}

	tx, err := w.pool.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	q := w.pool.Queries.WithTx(tx)
	for _, record := range records {
		if err := q.InsertSynthRequest(ctx, db.InsertSynthRequestParams{
			ID:         record.ID,
			TenantID:   record.TenantID,
			ApiKeyID:   record.KeyID,
			VoiceID:    record.VoiceID,
			CacheKey:   record.CacheKey,
			Mode:       record.Mode,
			Chars:      clampChars(record.Chars),
			DurationMs: nullableInt32(record.DurationMS),
			TtfaMs:     nullableInt32(record.TTFAMS),
			Cached:     record.Cached,
			Status:     record.Status,
			ErrorCode:  nullableString(record.ErrorCode),
			CreatedAt:  pgtype.Timestamptz{Time: record.CreatedAt, Valid: true},
		}); err != nil {
			return fmt.Errorf("insert usage record: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// clampChars keeps the value inside the column's range. A request cannot legitimately
// exceed it, and a wrapped value would corrupt an invoice.
func clampChars(chars int64) int32 {
	const maxChars = 1 << 30
	switch {
	case chars < 0:
		return 0
	case chars > maxChars:
		return maxChars
	default:
		return int32(chars)
	}
}

func nullableInt32(v int32) *int32 {
	if v == 0 {
		return nil
	}
	return &v
}
