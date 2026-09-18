//go:build integration

package postgres_test

import (
	"context"
	"crypto/sha256"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/pcaokhai/vitts/gateway/internal/storage/postgres"
	"github.com/pcaokhai/vitts/gateway/internal/storage/postgres/db"
	"github.com/pcaokhai/vitts/gateway/internal/storage/postgres/pgtest"
)

// fixture is a migrated database plus both ways of reaching it: the adapter under test
// and a raw pool for rows whose queries belong to a later task.
type fixture struct {
	pool *postgres.Pool
	raw  *pgxpool.Pool
}

func newFixture(t *testing.T) fixture {
	t.Helper()

	ctx := context.Background()
	url := pgtest.Start(t)

	pool, err := postgres.Open(ctx, url, 4)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	raw, err := pgxpool.New(ctx, url)
	require.NoError(t, err)
	t.Cleanup(raw.Close)

	return fixture{pool: pool, raw: raw}
}

// tenant inserts a plan and a tenant, the two rows nearly everything else references.
func (f fixture) tenant(t *testing.T) uuid.UUID {
	t.Helper()

	ctx := context.Background()
	planID := "test-" + uuid.NewString()[:8]
	_, err := f.raw.Exec(ctx,
		`insert into plans (id, chars_per_month, req_per_minute, max_concurrent_streams)
		 values ($1, 1000, 60, 2)`, planID)
	require.NoError(t, err)

	tenant, err := f.pool.Queries.CreateTenant(ctx, db.CreateTenantParams{
		ID:     uuid.New(),
		PlanID: planID,
		Name:   "Acme",
	})
	require.NoError(t, err)
	require.Equal(t, "active", tenant.Status)
	return tenant.ID
}

func hash(secret string) []byte {
	sum := sha256.Sum256([]byte(secret))
	return sum[:]
}

func TestMigrationsShipNoPricing(t *testing.T) {
	f := newFixture(t)

	plans, err := f.pool.Queries.ListPlans(context.Background())

	require.NoError(t, err)
	require.Empty(t, plans, "pricing is undecided; scripts/seed.sql is dev-only")
}

func TestReadyFailsOnAClosedPool(t *testing.T) {
	f := newFixture(t)

	require.NoError(t, f.pool.Ready(context.Background()))

	f.pool.Close()

	require.Error(t, f.pool.Ready(context.Background()))
}

func TestTenantRequiresAnExistingPlan(t *testing.T) {
	f := newFixture(t)

	_, err := f.pool.Queries.CreateTenant(context.Background(), db.CreateTenantParams{
		ID:     uuid.New(),
		PlanID: "nonexistent",
		Name:   "Acme",
	})

	require.Error(t, err, "plan_id is a foreign key")
}

func TestAPIKeyLookupIgnoresRevokedKeys(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	tenantID := f.tenant(t)

	key, err := f.pool.Queries.CreateAPIKey(ctx, db.CreateAPIKeyParams{
		ID:        uuid.New(),
		TenantID:  tenantID,
		Name:      "default",
		KeyHash:   hash("zt_test_secret"),
		Prefix:    "zt_test_ab12cd34",
		Scopes:    []string{"synthesize"},
		StoreText: false,
	})
	require.NoError(t, err)

	found, err := f.pool.Queries.GetAPIKeyByHash(ctx, hash("zt_test_secret"))
	require.NoError(t, err)
	require.Equal(t, key.ID, found.ID)
	require.Equal(t, tenantID, found.Tenant.ID)

	affected, err := f.pool.Queries.RevokeAPIKey(ctx, db.RevokeAPIKeyParams{
		ID: key.ID, TenantID: tenantID,
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), affected)

	_, err = f.pool.Queries.GetAPIKeyByHash(ctx, hash("zt_test_secret"))
	require.Error(t, err, "a revoked key must stop authenticating immediately (US-17)")
}

func TestRevokeIsScopedToTheOwningTenant(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	owner := f.tenant(t)
	other := f.tenant(t)

	key, err := f.pool.Queries.CreateAPIKey(ctx, db.CreateAPIKeyParams{
		ID: uuid.New(), TenantID: owner, Name: "k", KeyHash: hash("s1"),
		Prefix: "zt_test_1", Scopes: []string{"synthesize"},
	})
	require.NoError(t, err)

	affected, err := f.pool.Queries.RevokeAPIKey(ctx, db.RevokeAPIKeyParams{
		ID: key.ID, TenantID: other,
	})

	require.NoError(t, err)
	require.Zero(t, affected, "another tenant's key must be untouchable")
}

func TestKeyHashIsUnique(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	tenantID := f.tenant(t)

	_, err := f.pool.Queries.CreateAPIKey(ctx, db.CreateAPIKeyParams{
		ID: uuid.New(), TenantID: tenantID, Name: "a", KeyHash: hash("same"),
		Prefix: "zt_test_1", Scopes: []string{"synthesize"},
	})
	require.NoError(t, err)

	_, err = f.pool.Queries.CreateAPIKey(ctx, db.CreateAPIKeyParams{
		ID: uuid.New(), TenantID: tenantID, Name: "b", KeyHash: hash("same"),
		Prefix: "zt_test_2", Scopes: []string{"synthesize"},
	})

	require.Error(t, err)
}

func TestSynthRequestsRouteIntoTheMonthlyPartition(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	tenantID := f.tenant(t)

	now := time.Now().UTC()
	_, err := f.raw.Exec(ctx,
		`insert into synth_requests (id, tenant_id, api_key_id, voice_id, mode, chars, status, created_at)
		 values ($1, $2, $3, 'maichi', 'stream', 12, 'ok', $4)`,
		uuid.New(), tenantID, uuid.New(), now)
	require.NoError(t, err)

	var partition string
	require.NoError(t, f.raw.QueryRow(ctx,
		`select tableoid::regclass::text from synth_requests where tenant_id = $1`, tenantID,
	).Scan(&partition))

	require.Equal(t, "synth_requests_"+now.Format("200601"), partition)
}

func TestSynthRequestsRejectsARowWithNoPartition(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	tenantID := f.tenant(t)

	// A year out: the maintenance job in task 2.8 keeps partitions ahead, and a missing
	// one must fail loudly rather than silently drop metering data.
	_, err := f.raw.Exec(ctx,
		`insert into synth_requests (id, tenant_id, api_key_id, voice_id, mode, chars, status, created_at)
		 values ($1, $2, $3, 'maichi', 'sync', 12, 'ok', $4)`,
		uuid.New(), tenantID, uuid.New(), time.Now().UTC().AddDate(1, 0, 0))

	require.Error(t, err, "no partition exists for that month")
}

func TestPartitionHelperIsIdempotent(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	at := time.Now().UTC().AddDate(0, 6, 0)
	var first, second string
	require.NoError(t, f.raw.QueryRow(ctx, `select ensure_synth_requests_partition($1)`, at).Scan(&first))
	require.NoError(t, f.raw.QueryRow(ctx, `select ensure_synth_requests_partition($1)`, at).Scan(&second))

	require.Equal(t, first, second)
	require.Equal(t, "synth_requests_"+at.Format("200601"), first)
}
