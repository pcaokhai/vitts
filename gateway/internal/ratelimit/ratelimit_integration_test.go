//go:build integration

package ratelimit_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/pcaokhai/vitts/gateway/internal/ratelimit"
	redisadapter "github.com/pcaokhai/vitts/gateway/internal/storage/redis"
	"github.com/pcaokhai/vitts/gateway/internal/storage/redis/redistest"
)

func newLimiter(t *testing.T) *ratelimit.Limiter {
	t.Helper()
	limiter, _ := newLimiterWithClient(t)
	return limiter
}

func newLimiterWithClient(t *testing.T) (*ratelimit.Limiter, *goredis.Client) {
	t.Helper()

	ctx := context.Background()
	client, err := redisadapter.Open(ctx, redistest.Start(t), 32)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, client.Close()) })

	limiter, err := ratelimit.New(ctx, client.Raw())
	require.NoError(t, err)
	return limiter, client.Raw()
}

// T-105. A Redis restart empties the script cache. The limiter must reload the script
// instead of failing every later request with NOSCRIPT, which is what a SHA captured at
// boot did until the M3 runbook drill caught it (docs/reports/drill-m3.md, finding 1).
func TestLimiterSurvivesAFlushedScriptCache(t *testing.T) {
	ctx := context.Background()
	limiter, rdb := newLimiterWithClient(t)
	keyID, tenantID := uuid.New(), uuid.New()

	_, err := limiter.Allow(ctx, keyID, 60)
	require.NoError(t, err)

	require.NoError(t, rdb.ScriptFlush(ctx).Err())

	decision, err := limiter.Allow(ctx, keyID, 60)
	require.NoError(t, err, "rate limiting must recover from an empty script cache")
	require.True(t, decision.Allowed)

	lease, ok, err := limiter.Acquire(ctx, tenantID, 2)
	require.NoError(t, err, "leases must recover from an empty script cache")
	require.True(t, ok)

	require.NoError(t, rdb.ScriptFlush(ctx).Err())
	require.NoError(t, limiter.Renew(ctx, lease), "renew must recover from an empty script cache")
}

func TestFirstRequestOfAKeyIsNeverThrottled(t *testing.T) {
	limiter := newLimiter(t)

	decision, err := limiter.Allow(context.Background(), uuid.New(), 60)

	require.NoError(t, err)
	require.True(t, decision.Allowed)
	require.Equal(t, 60, decision.Limit)
	require.Equal(t, 59, decision.Remaining)
	require.Zero(t, decision.RetryAfter)
}

func TestBucketDrainsThenRefusesWithRetryAfter(t *testing.T) {
	limiter := newLimiter(t)
	ctx := context.Background()
	keyID := uuid.New()

	for i := range 5 {
		decision, err := limiter.Allow(ctx, keyID, 5)
		require.NoError(t, err)
		require.True(t, decision.Allowed, "request %d should fit in the bucket", i)
	}

	decision, err := limiter.Allow(ctx, keyID, 5)

	require.NoError(t, err)
	require.False(t, decision.Allowed)
	require.Positive(t, decision.RetryAfter, "a refused caller must be told when to come back")
	require.Zero(t, decision.Remaining)
}

// T-05: the limiter must be atomic, so exactly `limit` of N concurrent requests succeed.
func TestLimiterIsAtomicUnderConcurrency(t *testing.T) {
	limiter := newLimiter(t)
	ctx := context.Background()
	keyID := uuid.New()

	const (
		callers = 100
		limit   = 30
	)

	var allowed atomic.Int64
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start

			decision, err := limiter.Allow(ctx, keyID, limit)
			require.NoError(t, err)
			if decision.Allowed {
				allowed.Add(1)
			}
		}()
	}

	close(start)
	wg.Wait()

	require.Equal(t, int64(limit), allowed.Load(),
		"exactly the limit must pass; anything else means refill-test-consume interleaved")
}

func TestKeysAreIsolatedFromEachOther(t *testing.T) {
	limiter := newLimiter(t)
	ctx := context.Background()
	noisy, quiet := uuid.New(), uuid.New()

	for range 5 {
		_, err := limiter.Allow(ctx, noisy, 5)
		require.NoError(t, err)
	}

	decision, err := limiter.Allow(ctx, quiet, 5)

	require.NoError(t, err)
	require.True(t, decision.Allowed, "one key's traffic must not consume another's budget")
}

func TestLeasesAreCappedPerTenant(t *testing.T) {
	limiter := newLimiter(t)
	ctx := context.Background()
	tenantID := uuid.New()

	first, ok, err := limiter.Acquire(ctx, tenantID, 2)
	require.NoError(t, err)
	require.True(t, ok)

	_, ok, err = limiter.Acquire(ctx, tenantID, 2)
	require.NoError(t, err)
	require.True(t, ok)

	_, ok, err = limiter.Acquire(ctx, tenantID, 2)
	require.NoError(t, err)
	require.False(t, ok, "the third stream exceeds max_concurrent_streams")

	require.NoError(t, limiter.Release(ctx, first))

	_, ok, err = limiter.Acquire(ctx, tenantID, 2)
	require.NoError(t, err)
	require.True(t, ok, "releasing frees the slot")
}

func TestLeaseAcquisitionIsAtomicUnderConcurrency(t *testing.T) {
	limiter := newLimiter(t)
	ctx := context.Background()
	tenantID := uuid.New()

	const (
		callers = 100
		limit   = 10
	)

	var acquired atomic.Int64
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start

			_, ok, err := limiter.Acquire(ctx, tenantID, limit)
			require.NoError(t, err)
			if ok {
				acquired.Add(1)
			}
		}()
	}

	close(start)
	wg.Wait()

	require.Equal(t, int64(limit), acquired.Load())
}

func TestLeasesOfDifferentTenantsDoNotCompete(t *testing.T) {
	limiter := newLimiter(t)
	ctx := context.Background()
	a, b := uuid.New(), uuid.New()

	_, ok, err := limiter.Acquire(ctx, a, 1)
	require.NoError(t, err)
	require.True(t, ok)

	_, ok, err = limiter.Acquire(ctx, b, 1)

	require.NoError(t, err)
	require.True(t, ok)
}

func TestRenewKeepsALeaseAlive(t *testing.T) {
	limiter := newLimiter(t)
	ctx := context.Background()
	tenantID := uuid.New()

	lease, ok, err := limiter.Acquire(ctx, tenantID, 1)
	require.NoError(t, err)
	require.True(t, ok)

	require.NoError(t, limiter.Renew(ctx, lease))

	active, err := limiter.Active(ctx, tenantID)
	require.NoError(t, err)
	require.Equal(t, int64(1), active)
}

func TestRenewingAReclaimedLeaseReportsItLost(t *testing.T) {
	limiter := newLimiter(t)
	ctx := context.Background()
	tenantID := uuid.New()

	lease, ok, err := limiter.Acquire(ctx, tenantID, 1)
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, limiter.Release(ctx, lease))

	err = limiter.Renew(ctx, lease)

	require.ErrorIs(t, err, ratelimit.ErrLeaseLost,
		"a stream whose lease was reclaimed must learn it, not keep consuming capacity")
}

func TestReleasingAnUnknownLeaseIsNotAnError(t *testing.T) {
	limiter := newLimiter(t)

	err := limiter.Release(context.Background(), ratelimit.Lease{
		ID: uuid.NewString(), TenantID: uuid.New(),
	})

	require.NoError(t, err, "cleanup paths run twice; releasing twice must be safe")
}

func TestExpiredLeasesAreReclaimedWithoutASweeper(t *testing.T) {
	limiter := newLimiter(t)
	ctx := context.Background()
	tenantID := uuid.New()

	// A gateway that crashed holds a lease it will never release; the next acquisition
	// must evict it (US-06 acceptance criterion 3).
	limiter.SetClockForTest(func() time.Time { return time.Now().Add(-2 * ratelimit.LeaseTTL) })
	_, ok, err := limiter.Acquire(ctx, tenantID, 1)
	require.NoError(t, err)
	require.True(t, ok)

	limiter.SetClockForTest(time.Now)

	_, ok, err = limiter.Acquire(ctx, tenantID, 1)

	require.NoError(t, err)
	require.True(t, ok, "an expired lease must not hold capacity forever")
}
