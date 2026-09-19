//go:build integration

package quota_test

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/pcaokhai/vitts/gateway/internal/quota"
	redisadapter "github.com/pcaokhai/vitts/gateway/internal/storage/redis"
	"github.com/pcaokhai/vitts/gateway/internal/storage/redis/redistest"
)

const planLimit = 1000

func newService(t *testing.T) *quota.Service {
	t.Helper()

	client, err := redisadapter.Open(context.Background(), redistest.Start(t), 8)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, client.Close()) })

	return quota.New(redisadapter.NewCounter(client))
}

func TestFreshTenantHasFullAllowance(t *testing.T) {
	service := newService(t)

	decision, err := service.Check(context.Background(), uuid.New(), planLimit, 100)

	require.NoError(t, err)
	require.True(t, decision.Allowed)
	require.Zero(t, decision.Used)
	require.Equal(t, int64(planLimit*quota.GraceFactor), decision.Ceiling)
}

// US-07 acceptance criterion 1: the bound is limit x 1.05, not limit.
func TestGraceAllowsFivePercentOverTheLimit(t *testing.T) {
	service := newService(t)
	ctx := context.Background()
	tenantID := uuid.New()

	require.NoError(t, service.Record(ctx, tenantID, planLimit))

	within, err := service.Check(ctx, tenantID, planLimit, 50)
	require.NoError(t, err)
	require.True(t, within.Allowed, "a request inside the 5% grace must still be served")

	beyond, err := service.Check(ctx, tenantID, planLimit, 51)
	require.NoError(t, err)
	require.False(t, beyond.Allowed)
}

func TestCheckDoesNotConsumeQuota(t *testing.T) {
	service := newService(t)
	ctx := context.Background()
	tenantID := uuid.New()

	for range 100 {
		_, err := service.Check(ctx, tenantID, planLimit, 500)
		require.NoError(t, err)
	}

	used, err := service.Used(ctx, tenantID)

	require.NoError(t, err)
	require.Zero(t, used, "a failed synthesis must not bill the tenant")
}

func TestRecordAccumulates(t *testing.T) {
	service := newService(t)
	ctx := context.Background()
	tenantID := uuid.New()

	require.NoError(t, service.Record(ctx, tenantID, 300))
	require.NoError(t, service.Record(ctx, tenantID, 200))

	used, err := service.Used(ctx, tenantID)

	require.NoError(t, err)
	require.Equal(t, int64(500), used)
}

func TestPayAsYouGoHasNoCeiling(t *testing.T) {
	service := newService(t)
	ctx := context.Background()
	tenantID := uuid.New()
	require.NoError(t, service.Record(ctx, tenantID, 10_000_000))

	decision, err := service.Check(ctx, tenantID, 0, 1_000_000)

	require.NoError(t, err)
	require.True(t, decision.Allowed, "a zero allowance means metered, not blocked")
}

func TestTenantsAreIsolated(t *testing.T) {
	service := newService(t)
	ctx := context.Background()
	heavy, light := uuid.New(), uuid.New()
	require.NoError(t, service.Record(ctx, heavy, planLimit*2))

	decision, err := service.Check(ctx, light, planLimit, 10)

	require.NoError(t, err)
	require.True(t, decision.Allowed)
}

// stubSource stands in for Postgres so the reconcile logic is tested without one.
type stubSource struct {
	totals []quota.MonthlyUsage
	from   time.Time
}

func (s *stubSource) SumCharsSince(_ context.Context, from, _ time.Time) ([]quota.MonthlyUsage, error) {
	s.from = from
	return s.totals, nil
}

// T-06: reconcile corrects drift in either direction.
func TestReconcileCorrectsDrift(t *testing.T) {
	service := newService(t)
	ctx := context.Background()
	undercounted, overcounted, correct := uuid.New(), uuid.New(), uuid.New()

	require.NoError(t, service.Record(ctx, undercounted, 100)) // Redis lost some INCRBYs
	require.NoError(t, service.Record(ctx, overcounted, 900))  // Redis double-counted
	require.NoError(t, service.Record(ctx, correct, 400))

	source := &stubSource{totals: []quota.MonthlyUsage{
		{TenantID: undercounted, Chars: 500},
		{TenantID: overcounted, Chars: 400},
		{TenantID: correct, Chars: 400},
	}}

	require.NoError(t, quota.NewReconciler(service, source, zerolog.New(io.Discard)).RunOnce(ctx))

	for tenantID, want := range map[uuid.UUID]int64{
		undercounted: 500, overcounted: 400, correct: 400,
	} {
		used, err := service.Used(ctx, tenantID)
		require.NoError(t, err)
		require.Equal(t, want, used)
	}

	require.Equal(t, 1, source.from.Day(), "reconcile totals from the first of the month")
	require.Equal(t, time.UTC, source.from.Location(), "months are UTC")
}

func TestReconcileSeedsAColdCounter(t *testing.T) {
	service := newService(t)
	ctx := context.Background()
	tenantID := uuid.New()

	// Redis was flushed: the counter is absent but the tenant has used its allowance.
	source := &stubSource{totals: []quota.MonthlyUsage{{TenantID: tenantID, Chars: 900}}}
	require.NoError(t, quota.NewReconciler(service, source, zerolog.New(io.Discard)).RunOnce(ctx))

	decision, err := service.Check(ctx, tenantID, planLimit, 200)

	require.NoError(t, err)
	require.False(t, decision.Allowed, "a flushed Redis must not hand back a spent allowance")
}
