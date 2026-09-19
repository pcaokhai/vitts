package postgres

import (
	"context"
	"fmt"

	"github.com/pcaokhai/vitts/gateway/internal/cache"
	"github.com/pcaokhai/vitts/gateway/internal/storage/postgres/db"
)

// CacheCatalogue is the durable record of cached audio. It implements cache.Catalogue.
//
// Redis is the fast index; this table is what the eviction job (task 2.8) scans, so it
// must survive a Redis flush.
type CacheCatalogue struct {
	pool *Pool
}

// NewCacheCatalogue wires the repository.
func NewCacheCatalogue(pool *Pool) *CacheCatalogue { return &CacheCatalogue{pool: pool} }

// Record inserts or refreshes the catalogue row.
func (c *CacheCatalogue) Record(ctx context.Context, key cache.Key, entry cache.Entry) error {
	if err := c.pool.Queries.UpsertCacheEntry(ctx, db.UpsertCacheEntryParams{
		CacheKey:   key.Bytes(),
		S3Key:      entry.S3Key,
		Format:     entry.Format,
		DurationMs: entry.DurationMS,
		Bytes:      entry.Bytes,
	}); err != nil {
		return fmt.Errorf("upsert cache entry: %w", err)
	}
	return nil
}

// Touch records a hit.
func (c *CacheCatalogue) Touch(ctx context.Context, key cache.Key) error {
	if err := c.pool.Queries.TouchCacheEntry(ctx, key.Bytes()); err != nil {
		return fmt.Errorf("touch cache entry: %w", err)
	}
	return nil
}
