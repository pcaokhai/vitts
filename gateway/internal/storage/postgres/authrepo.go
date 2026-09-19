package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/pcaokhai/vitts/gateway/internal/auth"
)

// tenantStatusActive is the only status that may authenticate. A suspended or deleted
// tenant fails exactly like an unknown key (US-05 acceptance criterion 2).
const tenantStatusActive = "active"

// AuthRepository resolves API key digests. It implements auth.Repository.
type AuthRepository struct {
	pool *Pool
}

// NewAuthRepository wires the repository to a pool.
func NewAuthRepository(pool *Pool) *AuthRepository {
	return &AuthRepository{pool: pool}
}

// FindByHash returns the identity behind a key digest.
//
// The query already excludes revoked keys; the tenant status check is here. Every
// negative answer is auth.ErrUnauthorized with no detail, so a caller cannot distinguish
// "no such key" from "revoked" from "tenant suspended".
func (r *AuthRepository) FindByHash(ctx context.Context, hash []byte) (auth.Identity, error) {
	row, err := r.pool.Queries.GetAPIKeyByHash(ctx, hash)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return auth.Identity{}, auth.ErrUnauthorized
		}
		// A database failure is not an authentication decision: report it as such so the
		// caller returns 500 rather than a misleading 401.
		return auth.Identity{}, fmt.Errorf("%w: %w", auth.ErrLookupFailure, err)
	}

	if row.Tenant.Status != tenantStatusActive {
		return auth.Identity{}, auth.ErrUnauthorized
	}

	return auth.Identity{
		TenantID:       row.TenantID,
		KeyID:          row.ID,
		PlanID:         row.Plan.ID,
		Scopes:         toScopes(row.Scopes),
		StoreText:      row.StoreText,
		KeyFingerprint: auth.FingerprintForLogs(row.KeyHash),
	}, nil
}

func toScopes(raw []string) []auth.Scope {
	scopes := make([]auth.Scope, 0, len(raw))
	for _, s := range raw {
		scopes = append(scopes, auth.Scope(s))
	}
	return scopes
}
