// Package postgres is the outbound adapter for PostgreSQL. It owns the pool and the
// readiness probe; queries live in the sqlc-generated db package.
//
// Nothing above this package imports pgx (.claude/rules/gateway-go.md).
package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pcaokhai/vitts/gateway/internal/storage/postgres/db"
)

const (
	// connectTimeout bounds the initial dial so a wrong URL fails the boot fast instead
	// of hanging a deployment.
	connectTimeout = 10 * time.Second
	// pingTimeout bounds the readiness probe. /readyz is polled often; a slow database
	// must fail the check, not stall it.
	pingTimeout = 2 * time.Second
)

// Pool is a connection pool plus the queries it serves.
type Pool struct {
	pool    *pgxpool.Pool
	Queries *db.Queries
}

// Open dials the database and verifies the connection before returning.
//
// maxConns bounds the pool: everything in this gateway is bounded
// (docs/14-engineering-standards.md § Architecture rules).
func Open(ctx context.Context, url string, maxConns int32) (*Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	cfg.MaxConns = maxConns

	dialCtx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()

	pool, err := pgxpool.NewWithConfig(dialCtx, cfg)
	if err != nil {
		return nil, fmt.Errorf("open pool: %w", err)
	}
	if err := pool.Ping(dialCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}

	return &Pool{pool: pool, Queries: db.New(pool)}, nil
}

// Ready is the /readyz check for this dependency.
func (p *Pool) Ready(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, pingTimeout)
	defer cancel()

	if err := p.pool.Ping(ctx); err != nil {
		return fmt.Errorf("postgres: %w", err)
	}
	return nil
}

// Close releases every connection. Safe to call once, at shutdown.
func (p *Pool) Close() { p.pool.Close() }
