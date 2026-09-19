package redis

import (
	"context"
	"errors"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

// Counter implements quota.Counter over Redis.
type Counter struct {
	rdb *goredis.Client
}

// NewCounter wires the adapter.
func NewCounter(client *Client) *Counter { return &Counter{rdb: client.rdb} }

// Get returns the counter, or 0 when the key does not exist.
func (c *Counter) Get(ctx context.Context, key string) (int64, error) {
	value, err := c.rdb.Get(ctx, key).Int64()
	if errors.Is(err, goredis.Nil) {
		// An absent counter means "nothing used yet", not a failure.
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("get %s: %w", key, err)
	}
	return value, nil
}

// Add increments the counter and refreshes its TTL in one round trip, so a counter can
// never be left without an expiry by a crash between the two commands.
func (c *Counter) Add(ctx context.Context, key string, delta int64, ttl time.Duration) error {
	pipe := c.rdb.TxPipeline()
	pipe.IncrBy(ctx, key, delta)
	pipe.Expire(ctx, key, ttl)
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("add %s: %w", key, err)
	}
	return nil
}

// Set overwrites the counter.
func (c *Counter) Set(ctx context.Context, key string, value int64, ttl time.Duration) error {
	if err := c.rdb.Set(ctx, key, value, ttl).Err(); err != nil {
		return fmt.Errorf("set %s: %w", key, err)
	}
	return nil
}
