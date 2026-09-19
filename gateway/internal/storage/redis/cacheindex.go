package redis

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/pcaokhai/vitts/gateway/internal/cache"
)

// CacheIndex implements cache.Index over Redis (docs/05-data-model.md § 2).
type CacheIndex struct {
	rdb *goredis.Client
}

// NewCacheIndex wires the adapter.
func NewCacheIndex(client *Client) *CacheIndex { return &CacheIndex{rdb: client.rdb} }

// Get reads an index entry, refreshing its expiry so a hot entry does not expire under a
// steady stream of hits (the TTL is sliding, per the data model).
func (c *CacheIndex) Get(ctx context.Context, key cache.Key) (cache.Entry, error) {
	fields, err := c.rdb.HGetAll(ctx, indexKey(key)).Result()
	if err != nil {
		return cache.Entry{}, fmt.Errorf("read cache index: %w", err)
	}
	if len(fields) == 0 {
		return cache.Entry{}, cache.ErrMiss
	}

	duration, err := strconv.ParseInt(fields["duration_ms"], 10, 32)
	if err != nil {
		// A malformed entry is a miss, not an error: re-synthesising is always correct.
		return cache.Entry{}, cache.ErrMiss
	}
	size, err := strconv.ParseInt(fields["bytes"], 10, 64)
	if err != nil {
		return cache.Entry{}, cache.ErrMiss
	}

	if err := c.rdb.Expire(ctx, indexKey(key), cache.IndexTTL).Err(); err != nil {
		return cache.Entry{}, fmt.Errorf("refresh cache index ttl: %w", err)
	}

	return cache.Entry{
		S3Key:      fields["s3_key"],
		Format:     fields["format"],
		DurationMS: int32(duration),
		Bytes:      size,
	}, nil
}

// Put writes an index entry.
func (c *CacheIndex) Put(ctx context.Context, key cache.Key, entry cache.Entry, ttl time.Duration) error {
	pipe := c.rdb.TxPipeline()
	pipe.HSet(ctx, indexKey(key), map[string]any{
		"s3_key":      entry.S3Key,
		"format":      entry.Format,
		"duration_ms": entry.DurationMS,
		"bytes":       entry.Bytes,
	})
	pipe.Expire(ctx, indexKey(key), ttl)
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("write cache index: %w", err)
	}
	return nil
}

// Delete removes an index entry.
func (c *CacheIndex) Delete(ctx context.Context, key cache.Key) error {
	if err := c.rdb.Del(ctx, indexKey(key)).Err(); err != nil && !errors.Is(err, goredis.Nil) {
		return fmt.Errorf("delete cache index: %w", err)
	}
	return nil
}

func indexKey(key cache.Key) string { return "cache:" + key.String() }
