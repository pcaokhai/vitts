package postgres

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/pcaokhai/vitts/gateway/internal/auth"
	"github.com/pcaokhai/vitts/gateway/internal/storage/postgres/db"
)

// KeyRepository implements auth.KeyStore.
type KeyRepository struct {
	pool *Pool
}

// NewKeyRepository wires the repository.
func NewKeyRepository(pool *Pool) *KeyRepository { return &KeyRepository{pool: pool} }

// Create inserts a key row. The secret is not passed in; only its digest.
func (r *KeyRepository) Create(ctx context.Context, record auth.KeyRecord, hash []byte) (auth.KeyRecord, error) {
	scopes := make([]string, 0, len(record.Scopes))
	for _, scope := range record.Scopes {
		scopes = append(scopes, string(scope))
	}

	row, err := r.pool.Queries.CreateAPIKey(ctx, db.CreateAPIKeyParams{
		ID: record.ID, TenantID: record.TenantID, Name: record.Name,
		KeyHash: hash, Prefix: record.Prefix, Scopes: scopes, StoreText: record.StoreText,
	})
	if err != nil {
		return auth.KeyRecord{}, fmt.Errorf("insert api key: %w", err)
	}
	return toKeyRecord(row), nil
}

// List returns the tenant's keys.
func (r *KeyRepository) List(ctx context.Context, tenantID uuid.UUID, includeRevoked bool) ([]auth.KeyRecord, error) {
	rows, err := r.pool.Queries.ListAPIKeys(ctx, db.ListAPIKeysParams{
		TenantID: tenantID, IncludeRevoked: includeRevoked,
	})
	if err != nil {
		return nil, fmt.Errorf("list api keys: %w", err)
	}

	out := make([]auth.KeyRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, toKeyRecord(row))
	}
	return out, nil
}

// Revoke withdraws a key. False means it was not this tenant's, or was already revoked.
func (r *KeyRepository) Revoke(ctx context.Context, tenantID, keyID uuid.UUID) (bool, error) {
	affected, err := r.pool.Queries.RevokeAPIKey(ctx, db.RevokeAPIKeyParams{ID: keyID, TenantID: tenantID})
	if err != nil {
		return false, fmt.Errorf("revoke api key: %w", err)
	}
	return affected > 0, nil
}

func toKeyRecord(row db.ApiKey) auth.KeyRecord {
	scopes := make([]auth.Scope, 0, len(row.Scopes))
	for _, scope := range row.Scopes {
		scopes = append(scopes, auth.Scope(scope))
	}

	record := auth.KeyRecord{
		ID: row.ID, TenantID: row.TenantID, Name: row.Name, Prefix: row.Prefix,
		Scopes: scopes, StoreText: row.StoreText, CreatedAt: row.CreatedAt.Time,
	}
	if row.LastUsedAt.Valid {
		last := row.LastUsedAt.Time
		record.LastUsedAt = &last
	}
	if row.RevokedAt.Valid {
		revoked := row.RevokedAt.Time
		record.RevokedAt = &revoked
	}
	return record
}
