// Package ratelimit enforces the two per-tenant limits in US-06: a per-key request rate
// and a per-tenant concurrent-stream count.
//
// Both are Redis Lua scripts, because refill-test-consume and evict-count-insert must be
// atomic across replicas; doing them in Go would let two gateways admit the same last
// slot (docs/05-data-model.md § 2).
package ratelimit

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	goredis "github.com/redis/go-redis/v9"
)

// LeaseTTL is the lease lifetime from docs/05-data-model.md. It is renewed while a
// stream runs, so a crashed gateway releases capacity within one TTL.
const LeaseTTL = 60 * time.Second

// refillWindow is the period a plan's req_per_minute applies to.
const refillWindow = time.Minute

//go:embed lua/token_bucket.lua
var tokenBucketScript string

//go:embed lua/lease.lua
var leaseScript string

//go:embed lua/renew.lua
var renewScript string

// ErrLeaseLost means a renewal found no lease: it expired and the slot was reclaimed.
var ErrLeaseLost = errors.New("lease lost")

// Limiter runs the limit scripts.
type Limiter struct {
	rdb         *goredis.Client
	tokenBucket *goredis.Script
	lease       *goredis.Script
	renew       *goredis.Script
	leaseTTL    time.Duration
	nowOverride func() time.Time // tests only
}

// New prepares the limit scripts.
//
// Each call is EVALSHA so the script body stays off the hot path, but goredis.Script
// re-sends the body on NOSCRIPT. That fallback is the whole point: a Redis restart
// empties the script cache, and a limiter pinned to a SHA loaded at boot would answer
// every later request with an error until the gateway itself was restarted
// (docs/reports/drill-m3.md, finding 1).
func New(ctx context.Context, rdb *goredis.Client) (*Limiter, error) {
	l := &Limiter{
		rdb:         rdb,
		tokenBucket: goredis.NewScript(tokenBucketScript),
		lease:       goredis.NewScript(leaseScript),
		renew:       goredis.NewScript(renewScript),
		leaseTTL:    LeaseTTL,
	}

	// Preloading is an optimisation, not a requirement: it warms the cache so the first
	// request is a plain EVALSHA. A Redis that cannot be reached here fails the boot
	// probe anyway, so the error is still worth returning.
	for name, script := range map[string]*goredis.Script{
		"token_bucket": l.tokenBucket,
		"lease":        l.lease,
		"renew":        l.renew,
	} {
		if err := script.Load(ctx, rdb).Err(); err != nil {
			return nil, fmt.Errorf("load %s script: %w", name, err)
		}
	}

	return l, nil
}

func (l *Limiter) now() time.Time {
	if l.nowOverride != nil {
		return l.nowOverride()
	}
	return time.Now()
}

// RateDecision is the outcome of a rate-limit check.
type RateDecision struct {
	Allowed   bool
	Limit     int
	Remaining int
	// RetryAfter is how long until one token is available. Zero when allowed.
	RetryAfter time.Duration
}

// Allow consumes one token from the key's bucket.
//
// perMinute comes from the tenant's plan, so a plan change takes effect on the next
// request without touching Redis.
func (l *Limiter) Allow(ctx context.Context, keyID uuid.UUID, perMinute int32) (RateDecision, error) {
	if perMinute <= 0 {
		return RateDecision{}, fmt.Errorf("invalid rate limit %d", perMinute)
	}

	refillPerSec := float64(perMinute) / refillWindow.Seconds()
	res, err := l.tokenBucket.Run(ctx, l.rdb,
		[]string{rateKey(keyID)},
		perMinute, refillPerSec, l.now().UnixMilli(), 1,
	).Slice()
	if err != nil {
		return RateDecision{}, fmt.Errorf("token bucket: %w", err)
	}

	allowed, remaining, retryMS, err := triple(res)
	if err != nil {
		return RateDecision{}, err
	}

	return RateDecision{
		Allowed:    allowed == 1,
		Limit:      int(perMinute),
		Remaining:  int(remaining),
		RetryAfter: time.Duration(retryMS) * time.Millisecond,
	}, nil
}

// Lease is a held concurrency slot.
type Lease struct {
	ID       string
	TenantID uuid.UUID
}

// Acquire takes a concurrency slot for the tenant.
//
// Returns ok=false rather than an error when the tenant is at its limit: being at a limit
// is a normal outcome the caller turns into 429, not a failure.
func (l *Limiter) Acquire(ctx context.Context, tenantID uuid.UUID, limit int32) (Lease, bool, error) {
	if limit <= 0 {
		return Lease{}, false, fmt.Errorf("invalid concurrency limit %d", limit)
	}

	lease := Lease{ID: uuid.NewString(), TenantID: tenantID}
	res, err := l.lease.Run(ctx, l.rdb,
		[]string{concKey(tenantID)},
		limit, l.now().UnixMilli(), l.leaseTTL.Milliseconds(), lease.ID,
	).Slice()
	if err != nil {
		return Lease{}, false, fmt.Errorf("acquire lease: %w", err)
	}
	if len(res) < 1 {
		return Lease{}, false, errors.New("acquire lease: malformed reply")
	}

	acquired, ok := res[0].(int64)
	if !ok {
		return Lease{}, false, errors.New("acquire lease: malformed reply")
	}
	if acquired != 1 {
		return Lease{}, false, nil
	}
	return lease, true, nil
}

// Renew extends a lease while its stream is still running.
func (l *Limiter) Renew(ctx context.Context, lease Lease) error {
	res, err := l.renew.Run(ctx, l.rdb,
		[]string{concKey(lease.TenantID)},
		l.now().UnixMilli(), l.leaseTTL.Milliseconds(), lease.ID,
	).Int64()
	if err != nil {
		return fmt.Errorf("renew lease: %w", err)
	}
	if res != 1 {
		return ErrLeaseLost
	}
	return nil
}

// Release returns a slot. It is safe to call for a lease that already expired.
func (l *Limiter) Release(ctx context.Context, lease Lease) error {
	if err := l.rdb.ZRem(ctx, concKey(lease.TenantID), lease.ID).Err(); err != nil {
		return fmt.Errorf("release lease: %w", err)
	}
	return nil
}

// Active reports how many leases a tenant currently holds, for metrics and tests.
func (l *Limiter) Active(ctx context.Context, tenantID uuid.UUID) (int64, error) {
	count, err := l.rdb.ZCount(ctx, concKey(tenantID), fmt.Sprint(l.now().UnixMilli()), "+inf").Result()
	if err != nil {
		return 0, fmt.Errorf("count leases: %w", err)
	}
	return count, nil
}

func rateKey(keyID uuid.UUID) string { return "rl:" + keyID.String() }

func concKey(tenantID uuid.UUID) string { return "conc:" + tenantID.String() }

func triple(res []any) (a, b, c int64, err error) {
	if len(res) < 3 {
		return 0, 0, 0, errors.New("malformed script reply")
	}
	values := [3]int64{}
	for i := range values {
		v, ok := res[i].(int64)
		if !ok {
			return 0, 0, 0, errors.New("malformed script reply")
		}
		values[i] = v
	}
	return values[0], values[1], values[2], nil
}
