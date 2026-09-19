// Package redis is the outbound adapter for Redis: the client, its readiness probe, and
// the script cache every limiter uses.
//
// Nothing above this package imports go-redis (.claude/rules/gateway-go.md).
package redis

import (
	"context"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

const (
	// dialTimeout bounds the initial connection so a wrong URL fails the boot fast.
	dialTimeout = 5 * time.Second
	// pingTimeout bounds the readiness probe; /readyz is polled often.
	pingTimeout = 2 * time.Second
)

// Client wraps the Redis connection.
type Client struct {
	rdb *goredis.Client
}

// Open parses the URL, connects and verifies the connection.
//
// poolSize bounds concurrent connections: everything in this gateway is bounded
// (docs/14-engineering-standards.md).
func Open(ctx context.Context, url string, poolSize int) (*Client, error) {
	opts, err := goredis.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("parse redis url: %w", err)
	}
	opts.PoolSize = poolSize
	opts.DialTimeout = dialTimeout

	rdb := goredis.NewClient(opts)

	pingCtx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()
	if err := rdb.Ping(pingCtx).Err(); err != nil {
		_ = rdb.Close()
		return nil, fmt.Errorf("ping redis: %w", err)
	}

	return &Client{rdb: rdb}, nil
}

// Raw exposes the underlying client to adapters in this repository that need it.
func (c *Client) Raw() *goredis.Client { return c.rdb }

// Ready is the /readyz check for this dependency.
func (c *Client) Ready(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, pingTimeout)
	defer cancel()

	if err := c.rdb.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("redis: %w", err)
	}
	return nil
}

// Close releases the connection pool.
func (c *Client) Close() error {
	if err := c.rdb.Close(); err != nil {
		return fmt.Errorf("close redis: %w", err)
	}
	return nil
}
