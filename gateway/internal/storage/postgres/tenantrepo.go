package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/pcaokhai/vitts/gateway/internal/storage/postgres/db"
	"github.com/pcaokhai/vitts/gateway/internal/tenants"
)

// TenantRepository implements tenants.Repository.
type TenantRepository struct {
	pool *Pool
}

// NewTenantRepository wires the repository to a pool.
func NewTenantRepository(pool *Pool) *TenantRepository {
	return &TenantRepository{pool: pool}
}

// PlanExists reports whether the plan id is known.
func (r *TenantRepository) PlanExists(ctx context.Context, planID string) (bool, error) {
	_, err := r.pool.Queries.GetPlan(ctx, planID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("get plan: %w", err)
	}
	return true, nil
}

// CreateTenantWithKey commits the tenant, its first key and the audit entry together.
func (r *TenantRepository) CreateTenantWithKey(
	ctx context.Context,
	tenant tenants.Tenant,
	key tenants.KeyRecord,
	actor string,
) error {
	tx, err := r.pool.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	// Rollback after a successful commit is a no-op, so this needs no success flag.
	defer func() { _ = tx.Rollback(ctx) }()

	q := r.pool.Queries.WithTx(tx)

	if _, err := q.CreateTenant(ctx, db.CreateTenantParams{
		ID:     tenant.ID,
		PlanID: tenant.PlanID,
		Name:   tenant.Name,
	}); err != nil {
		return fmt.Errorf("insert tenant: %w", err)
	}

	scopes := make([]string, 0, len(key.Scopes))
	for _, scope := range key.Scopes {
		scopes = append(scopes, string(scope))
	}

	if _, err := q.CreateAPIKey(ctx, db.CreateAPIKeyParams{
		ID:        key.ID,
		TenantID:  key.TenantID,
		Name:      key.Name,
		KeyHash:   key.Hash,
		Prefix:    key.Prefix,
		Scopes:    scopes,
		StoreText: key.StoreText,
	}); err != nil {
		return fmt.Errorf("insert api key: %w", err)
	}

	target := tenant.ID.String()
	if err := q.CreateAuditEntry(ctx, db.CreateAuditEntryParams{
		ID:     uuid.New(),
		Actor:  actor,
		Action: "tenant.create",
		Target: &target,
		Detail: []byte(`{}`),
	}); err != nil {
		return fmt.Errorf("insert audit entry: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}
