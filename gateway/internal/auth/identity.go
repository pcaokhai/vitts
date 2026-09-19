package auth

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/google/uuid"
)

// Scope is a capability granted to a key. The set is docs/07-permissions.md § Scopes.
type Scope string

// The complete scope set.
const (
	ScopeSynth Scope = "synth"
	ScopeJobs  Scope = "jobs"
	ScopeUsage Scope = "usage"
	ScopeKeys  Scope = "keys"
)

// DefaultTenantScopes is what the first key of a new tenant receives. Keys created later
// default to [ScopeSynth] (docs/07-permissions.md).
var DefaultTenantScopes = []Scope{ScopeSynth, ScopeJobs, ScopeUsage, ScopeKeys}

// Errors a caller must be able to distinguish. Everything that means "you are not
// authenticated" is deliberately one error, so a prober cannot tell a revoked key from
// an unknown one or a suspended tenant.
var (
	ErrUnauthorized  = errors.New("unauthorized")
	ErrScopeMissing  = errors.New("scope missing")
	ErrLookupFailure = errors.New("key lookup failed")
)

// Identity is who the caller is, once authenticated. It carries no secret.
type Identity struct {
	TenantID  uuid.UUID
	KeyID     uuid.UUID
	PlanID    string
	Scopes    []Scope
	StoreText bool
	// KeyFingerprint is safe to log; the secret and the full hash are not.
	KeyFingerprint string
}

// Has reports whether the key carries a scope.
func (i Identity) Has(scope Scope) bool { return slices.Contains(i.Scopes, scope) }

// Require returns ErrScopeMissing unless the key carries the scope.
func (i Identity) Require(scope Scope) error {
	if i.Has(scope) {
		return nil
	}
	return fmt.Errorf("%w: %s", ErrScopeMissing, scope)
}

// Repository resolves a key digest to an identity. Implemented by the Postgres adapter;
// a Redis-backed cache wraps it in task 1.4 (US-05 acceptance criterion 5).
type Repository interface {
	// FindByHash returns ErrUnauthorized when no live key matches, when the tenant is
	// not active, or when the key is revoked. It must not distinguish those cases.
	FindByHash(ctx context.Context, hash []byte) (Identity, error)
}

// Authenticator turns a presented secret into an Identity.
type Authenticator struct {
	repo Repository
}

// NewAuthenticator wires the lookup.
func NewAuthenticator(repo Repository) *Authenticator {
	return &Authenticator{repo: repo}
}

// Authenticate resolves a bearer secret.
//
// Every failure below returns ErrUnauthorized with no detail: a caller learns whether it
// holds a working key and nothing else.
func (a *Authenticator) Authenticate(ctx context.Context, secret string) (Identity, error) {
	if _, _, ok := splitPrefix(secret); !ok {
		return Identity{}, ErrUnauthorized
	}

	identity, err := a.repo.FindByHash(ctx, Hash(secret))
	if err != nil {
		return Identity{}, err
	}
	return identity, nil
}
