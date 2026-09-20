package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// ErrKeyNotFound means the key does not belong to this tenant, or does not exist. The
// two are deliberately the same answer (docs/07-permissions.md).
var ErrKeyNotFound = errors.New("key not found")

// ErrInvalidKeyRequest means the request cannot produce a key.
var ErrInvalidKeyRequest = errors.New("invalid key request")

// maxKeyNameLen matches the schema's constraint.
const maxKeyNameLen = 200

// KeyRecord is a key as an operator sees it. It never carries the secret.
type KeyRecord struct {
	ID         uuid.UUID
	TenantID   uuid.UUID
	Name       string
	Prefix     string
	Scopes     []Scope
	StoreText  bool
	CreatedAt  time.Time
	LastUsedAt *time.Time
	RevokedAt  *time.Time
}

// Revoked reports whether the key has been withdrawn.
func (k KeyRecord) Revoked() bool { return k.RevokedAt != nil }

// KeyStore is the storage port for tenant key management.
type KeyStore interface {
	Create(ctx context.Context, record KeyRecord, hash []byte) (KeyRecord, error)
	List(ctx context.Context, tenantID uuid.UUID, includeRevoked bool) ([]KeyRecord, error)
	Revoke(ctx context.Context, tenantID, keyID uuid.UUID) (bool, error)
}

// KeyCache is the auth lookup cache, if one is in front of the store. Revoking must
// evict immediately rather than wait out a TTL (US-05 acceptance criterion 5).
type KeyCache interface {
	Forget(ctx context.Context, tenantID uuid.UUID)
}

// Keys manages a tenant's own API keys (US-17).
type Keys struct {
	store KeyStore
	cache KeyCache
}

// NewKeys wires the use case. A nil cache means nothing to evict.
func NewKeys(store KeyStore, cache KeyCache) *Keys {
	return &Keys{store: store, cache: cache}
}

// NewKeyScopes is what a key created after the first one receives
// (docs/07-permissions.md).
var NewKeyScopes = []Scope{ScopeSynth}

// Created is a freshly minted key. Secret appears exactly once, here.
type Created struct {
	Record KeyRecord
	Secret string
}

// Create mints a key for the tenant.
//
// Scopes are validated against the known set and may never exceed what the caller's own
// key carries: a key must not be able to mint a more powerful one.
func (k *Keys) Create(ctx context.Context, caller Identity, name string, scopes []Scope) (Created, error) {
	if name == "" || len(name) > maxKeyNameLen {
		return Created{}, fmt.Errorf("%w: name must be 1-%d characters", ErrInvalidKeyRequest, maxKeyNameLen)
	}

	if len(scopes) == 0 {
		scopes = NewKeyScopes
	}
	for _, scope := range scopes {
		if !knownScope(scope) {
			return Created{}, fmt.Errorf("%w: unknown scope %q", ErrInvalidKeyRequest, scope)
		}
		if !caller.Has(scope) {
			return Created{}, fmt.Errorf("%w: cannot grant %q, which this key does not hold",
				ErrInvalidKeyRequest, scope)
		}
	}

	generated, err := Generate(PrefixLive)
	if err != nil {
		return Created{}, fmt.Errorf("generate key: %w", err)
	}

	record, err := k.store.Create(ctx, KeyRecord{
		ID: uuid.New(), TenantID: caller.TenantID, Name: name,
		Prefix: generated.Prefix, Scopes: scopes,
	}, generated.Hash)
	if err != nil {
		return Created{}, fmt.Errorf("create key: %w", err)
	}

	return Created{Record: record, Secret: generated.Secret}, nil
}

// List returns the tenant's live keys.
func (k *Keys) List(ctx context.Context, tenantID uuid.UUID, includeRevoked bool) ([]KeyRecord, error) {
	records, err := k.store.List(ctx, tenantID, includeRevoked)
	if err != nil {
		return nil, fmt.Errorf("list keys: %w", err)
	}
	return records, nil
}

// Revoke withdraws a key.
//
// The auth cache is evicted immediately, so revocation takes effect now rather than
// after the cache TTL (US-05 acceptance criterion 5, US-17).
func (k *Keys) Revoke(ctx context.Context, caller Identity, keyID uuid.UUID) error {
	if keyID == caller.KeyID {
		// Allowed, and deliberate: an operator whose key leaked must be able to revoke
		// it with that very key, which is the only credential they still hold.
		_ = keyID
	}

	revoked, err := k.store.Revoke(ctx, caller.TenantID, keyID)
	if err != nil {
		return fmt.Errorf("revoke key: %w", err)
	}
	if !revoked {
		return ErrKeyNotFound
	}

	if k.cache != nil {
		k.cache.Forget(ctx, caller.TenantID)
	}
	return nil
}

func knownScope(scope Scope) bool {
	switch scope {
	case ScopeSynth, ScopeJobs, ScopeUsage, ScopeKeys:
		return true
	default:
		return false
	}
}
