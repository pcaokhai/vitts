// Package tenants is the admin-side use case for tenant lifecycle. It knows nothing about
// HTTP or SQL: both arrive through ports (.claude/rules/gateway-go.md).
package tenants

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/pcaokhai/vitts/gateway/internal/auth"
)

// Errors the transport maps to a status.
var (
	ErrUnknownPlan  = errors.New("unknown plan")
	ErrInvalidInput = errors.New("invalid input")
)

const maxNameLen = 200

// Tenant is what the admin API returns after creation.
type Tenant struct {
	ID     uuid.UUID
	Name   string
	PlanID string
	Status string
}

// NewKey is the first key of a tenant. Secret is present exactly once, in the creation
// response, and is not recoverable afterwards.
type NewKey struct {
	ID     uuid.UUID
	Prefix string
	Secret string
	Scopes []auth.Scope
}

// Repository is the storage port this use case needs.
type Repository interface {
	PlanExists(ctx context.Context, planID string) (bool, error)
	// CreateTenantWithKey inserts the tenant, its first key and the audit entry in one
	// transaction. A tenant with no key is unusable, a key with no tenant is
	// unreachable, and an unattributable tenant is an audit gap - none of the three may
	// exist on its own.
	CreateTenantWithKey(ctx context.Context, tenant Tenant, key KeyRecord, actor string) error
}

// KeyRecord is the storable half of a key: never the secret.
type KeyRecord struct {
	ID        uuid.UUID
	TenantID  uuid.UUID
	Name      string
	Hash      []byte
	Prefix    string
	Scopes    []auth.Scope
	StoreText bool
}

// Service creates tenants.
type Service struct {
	repo Repository
}

// NewService wires the use case.
func NewService(repo Repository) *Service { return &Service{repo: repo} }

// Create makes a tenant and its first key.
//
// The key is minted here rather than by the caller so the secret exists in exactly one
// place, and the audit entry is written in the same call because tenant creation is an
// operator action that must be attributable (docs/07-permissions.md).
func (s *Service) Create(ctx context.Context, name, planID, actor string) (Tenant, NewKey, error) {
	if name == "" || len(name) > maxNameLen {
		return Tenant{}, NewKey{}, fmt.Errorf("%w: name must be 1-%d characters", ErrInvalidInput, maxNameLen)
	}
	if planID == "" {
		return Tenant{}, NewKey{}, fmt.Errorf("%w: plan_id is required", ErrInvalidInput)
	}

	exists, err := s.repo.PlanExists(ctx, planID)
	if err != nil {
		return Tenant{}, NewKey{}, fmt.Errorf("look up plan: %w", err)
	}
	if !exists {
		return Tenant{}, NewKey{}, fmt.Errorf("%w: %s", ErrUnknownPlan, planID)
	}

	generated, err := auth.Generate(auth.PrefixLive)
	if err != nil {
		return Tenant{}, NewKey{}, fmt.Errorf("generate key: %w", err)
	}

	tenant := Tenant{ID: uuid.New(), Name: name, PlanID: planID, Status: "active"}
	record := KeyRecord{
		ID:       uuid.New(),
		TenantID: tenant.ID,
		Name:     "default",
		Hash:     generated.Hash,
		Prefix:   generated.Prefix,
		Scopes:   auth.DefaultTenantScopes,
	}

	// Nothing is returned to the operator unless all three rows committed, so a secret
	// is never handed out for a tenant that does not exist.
	if err := s.repo.CreateTenantWithKey(ctx, tenant, record, actor); err != nil {
		return Tenant{}, NewKey{}, fmt.Errorf("create tenant: %w", err)
	}

	return tenant, NewKey{
		ID:     record.ID,
		Prefix: generated.Prefix,
		Secret: generated.Secret,
		Scopes: record.Scopes,
	}, nil
}
