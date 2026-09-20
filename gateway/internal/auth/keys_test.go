package auth_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/pcaokhai/vitts/gateway/internal/auth"
)

type memKeyStore struct {
	mu      sync.Mutex
	records map[uuid.UUID]auth.KeyRecord
	hashes  map[uuid.UUID][]byte
}

func newMemKeyStore() *memKeyStore {
	return &memKeyStore{records: map[uuid.UUID]auth.KeyRecord{}, hashes: map[uuid.UUID][]byte{}}
}

func (m *memKeyStore) Create(_ context.Context, record auth.KeyRecord, hash []byte) (auth.KeyRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	record.CreatedAt = time.Now().UTC()
	m.records[record.ID], m.hashes[record.ID] = record, hash
	return record, nil
}

func (m *memKeyStore) List(_ context.Context, tenantID uuid.UUID, includeRevoked bool) ([]auth.KeyRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var out []auth.KeyRecord
	for _, record := range m.records {
		if record.TenantID != tenantID {
			continue
		}
		if record.Revoked() && !includeRevoked {
			continue
		}
		out = append(out, record)
	}
	return out, nil
}

func (m *memKeyStore) Revoke(_ context.Context, tenantID, keyID uuid.UUID) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	record, ok := m.records[keyID]
	if !ok || record.TenantID != tenantID || record.Revoked() {
		return false, nil
	}
	now := time.Now().UTC()
	record.RevokedAt = &now
	m.records[keyID] = record
	return true, nil
}

type recordingCache struct {
	mu        sync.Mutex
	forgotten []uuid.UUID
}

func (c *recordingCache) Forget(_ context.Context, tenantID uuid.UUID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.forgotten = append(c.forgotten, tenantID)
}

func owner(scopes ...auth.Scope) auth.Identity {
	return auth.Identity{TenantID: uuid.New(), KeyID: uuid.New(), Scopes: scopes}
}

func TestCreateReturnsTheSecretExactlyOnce(t *testing.T) {
	t.Parallel()

	store := newMemKeyStore()
	keys := auth.NewKeys(store, nil)
	caller := owner(auth.DefaultTenantScopes...)

	created, err := keys.Create(context.Background(), caller, "ci", []auth.Scope{auth.ScopeSynth})

	require.NoError(t, err)
	require.NotEmpty(t, created.Secret)
	require.Equal(t, auth.Hash(created.Secret), store.hashes[created.Record.ID],
		"only the digest is stored")

	listed, err := keys.List(context.Background(), caller.TenantID, false)
	require.NoError(t, err)
	require.Len(t, listed, 1)
	require.NotContains(t, listed[0].Prefix, created.Secret[len(created.Record.Prefix):],
		"a listing never reveals the secret")
}

func TestNewKeysDefaultToSynthOnly(t *testing.T) {
	t.Parallel()

	keys := auth.NewKeys(newMemKeyStore(), nil)

	created, err := keys.Create(context.Background(), owner(auth.DefaultTenantScopes...), "ci", nil)

	require.NoError(t, err)
	require.Equal(t, auth.NewKeyScopes, created.Record.Scopes)
}

// A key must not be able to mint one more powerful than itself.
func TestAKeyCannotGrantScopesItDoesNotHold(t *testing.T) {
	t.Parallel()

	keys := auth.NewKeys(newMemKeyStore(), nil)
	caller := owner(auth.ScopeKeys, auth.ScopeSynth)

	_, err := keys.Create(context.Background(), caller, "escalate", []auth.Scope{auth.ScopeJobs})

	require.ErrorIs(t, err, auth.ErrInvalidKeyRequest)
	require.Contains(t, err.Error(), "does not hold")
}

func TestUnknownScopeIsRefused(t *testing.T) {
	t.Parallel()

	keys := auth.NewKeys(newMemKeyStore(), nil)

	_, err := keys.Create(context.Background(), owner(auth.DefaultTenantScopes...), "ci",
		[]auth.Scope{"admin"})

	require.ErrorIs(t, err, auth.ErrInvalidKeyRequest)
}

// T-18: revoking must take effect at once, not after a cache TTL.
func TestRevokeEvictsTheAuthCache(t *testing.T) {
	t.Parallel()

	store, cache := newMemKeyStore(), &recordingCache{}
	keys := auth.NewKeys(store, cache)
	caller := owner(auth.DefaultTenantScopes...)
	created, err := keys.Create(context.Background(), caller, "ci", nil)
	require.NoError(t, err)

	require.NoError(t, keys.Revoke(context.Background(), caller, created.Record.ID))

	live, err := keys.List(context.Background(), caller.TenantID, false)
	require.NoError(t, err)
	require.Empty(t, live, "a revoked key is gone from the default listing")

	all, err := keys.List(context.Background(), caller.TenantID, true)
	require.NoError(t, err)
	require.Len(t, all, 1)
	require.True(t, all[0].Revoked())

	require.Equal(t, []uuid.UUID{caller.TenantID}, cache.forgotten,
		"revocation must not wait out the auth cache TTL")
}

func TestRevokingAnotherTenantsKeyIsNotFound(t *testing.T) {
	t.Parallel()

	store := newMemKeyStore()
	keys := auth.NewKeys(store, nil)
	ownerIdentity := owner(auth.DefaultTenantScopes...)
	created, err := keys.Create(context.Background(), ownerIdentity, "ci", nil)
	require.NoError(t, err)

	intruder := owner(auth.DefaultTenantScopes...)
	err = keys.Revoke(context.Background(), intruder, created.Record.ID)

	require.ErrorIs(t, err, auth.ErrKeyNotFound)
}

func TestRevokingTwiceReportsNotFound(t *testing.T) {
	t.Parallel()

	keys := auth.NewKeys(newMemKeyStore(), nil)
	caller := owner(auth.DefaultTenantScopes...)
	created, err := keys.Create(context.Background(), caller, "ci", nil)
	require.NoError(t, err)
	require.NoError(t, keys.Revoke(context.Background(), caller, created.Record.ID))

	err = keys.Revoke(context.Background(), caller, created.Record.ID)

	require.ErrorIs(t, err, auth.ErrKeyNotFound)
}

func TestNameIsRequired(t *testing.T) {
	t.Parallel()

	keys := auth.NewKeys(newMemKeyStore(), nil)

	_, err := keys.Create(context.Background(), owner(auth.DefaultTenantScopes...), "", nil)

	require.ErrorIs(t, err, auth.ErrInvalidKeyRequest)
}
