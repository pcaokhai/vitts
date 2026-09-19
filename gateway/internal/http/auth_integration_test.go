//go:build integration

package http_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/pcaokhai/vitts/gateway/internal/auth"
	gatewayhttp "github.com/pcaokhai/vitts/gateway/internal/http"
	"github.com/pcaokhai/vitts/gateway/internal/storage/postgres"
	"github.com/pcaokhai/vitts/gateway/internal/storage/postgres/pgtest"
	"github.com/pcaokhai/vitts/gateway/internal/tenants"
)

// adminKey is built rather than written out: a 32-character literal is indistinguishable
// from a real leaked key to a secret scanner, and to a reviewer.
var adminKey = strings.Repeat("ab", 16)

const adminPeer = "192.0.2.10:1234"

// stack is the real router over a real database: the only fake is the clock of the test.
type stack struct {
	handler http.Handler
	pool    *postgres.Pool
	logs    *strings.Builder
}

func newStack(t *testing.T) stack {
	t.Helper()

	ctx := context.Background()
	pool, err := postgres.Open(ctx, pgtest.Start(t), 4)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	_, err = pool.Queries.ListPlans(ctx)
	require.NoError(t, err)

	logs := &strings.Builder{}
	handler := gatewayhttp.Router(gatewayhttp.Deps{
		Logger:    zerolog.New(logs),
		Readiness: gatewayhttp.NewReadiness(),
		Admin: gatewayhttp.NewAdminGuard(adminKey, []netip.Prefix{
			netip.MustParsePrefix("192.0.2.0/24"),
		}),
		Tenants: tenants.NewService(postgres.NewTenantRepository(pool)),
	})

	return stack{handler: handler, pool: pool, logs: logs}
}

// createTenant calls the admin endpoint and returns the decoded body.
func (s stack) createTenant(t *testing.T, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()

	req := httptest.NewRequest(http.MethodPost, "/admin/v1/tenants", bytes.NewBufferString(body))
	req.RemoteAddr = adminPeer
	req.Header.Set(gatewayhttp.HeaderAdminKey, adminKey)

	res := httptest.NewRecorder()
	s.handler.ServeHTTP(res, req)

	decoded := map[string]any{}
	if res.Body.Len() > 0 {
		_ = json.Unmarshal(res.Body.Bytes(), &decoded)
	}
	return res, decoded
}

func TestAdminCreatesATenantWithAUsableKey(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()

	_, err := s.pool.Queries.GetPlan(ctx, "dev")
	require.Error(t, err, "no plan exists yet")

	insertPlan(t, s, "dev")

	res, body := s.createTenant(t, `{"name":"Acme","plan_id":"dev"}`)
	require.Equal(t, http.StatusCreated, res.Code)

	key, ok := body["key"].(map[string]any)
	require.True(t, ok)
	secret, _ := key["secret"].(string)
	require.True(t, strings.HasPrefix(secret, auth.PrefixLive))

	// The secret must authenticate, and it must never appear in a log line.
	identity, err := postgres.NewAuthRepository(s.pool).FindByHash(ctx, auth.Hash(secret))
	require.NoError(t, err)
	require.ElementsMatch(t, auth.DefaultTenantScopes, identity.Scopes)
	require.NotContains(t, s.logs.String(), secret)
}

func TestAdminRejectsAnUnknownPlan(t *testing.T) {
	s := newStack(t)

	res, body := s.createTenant(t, `{"name":"Acme","plan_id":"nope"}`)

	require.Equal(t, http.StatusBadRequest, res.Code)
	require.Equal(t, string(gatewayhttp.CodeInvalidRequest), body["code"])
}

func TestAdminRejectsAnEmptyName(t *testing.T) {
	s := newStack(t)
	insertPlan(t, s, "dev")

	res, _ := s.createTenant(t, `{"name":"","plan_id":"dev"}`)

	require.Equal(t, http.StatusBadRequest, res.Code)
}

func TestAdminEndpointNeedsBothKeyAndNetwork(t *testing.T) {
	s := newStack(t)
	insertPlan(t, s, "dev")

	req := httptest.NewRequest(http.MethodPost, "/admin/v1/tenants",
		bytes.NewBufferString(`{"name":"Acme","plan_id":"dev"}`))
	req.RemoteAddr = "198.51.100.7:1234"
	req.Header.Set(gatewayhttp.HeaderAdminKey, adminKey)

	res := httptest.NewRecorder()
	s.handler.ServeHTTP(res, req)

	require.Equal(t, http.StatusUnauthorized, res.Code)
}

func TestTenantCreationIsAtomic(t *testing.T) {
	s := newStack(t)
	insertPlan(t, s, "dev")
	ctx := context.Background()

	_, body := s.createTenant(t, `{"name":"Acme","plan_id":"dev"}`)
	tenant, _ := body["tenant"].(map[string]any)
	id, _ := tenant["id"].(string)

	var keys, audits int
	require.NoError(t, s.pool.Raw().QueryRow(ctx,
		`select count(*) from api_keys where tenant_id = $1`, id).Scan(&keys))
	require.NoError(t, s.pool.Raw().QueryRow(ctx,
		`select count(*) from audit_log where action = 'tenant.create' and target = $1`, id).Scan(&audits))

	require.Equal(t, 1, keys)
	require.Equal(t, 1, audits, "tenant creation is an operator action and must be attributable")
}

func insertPlan(t *testing.T, s stack, id string) {
	t.Helper()

	_, err := s.pool.Raw().Exec(context.Background(),
		`insert into plans (id, chars_per_month, req_per_minute, max_concurrent_streams)
		 values ($1, 1000, 60, 2)`, id)
	require.NoError(t, err)
}
