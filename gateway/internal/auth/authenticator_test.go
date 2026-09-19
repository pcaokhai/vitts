package auth_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/pcaokhai/vitts/gateway/internal/auth"
)

// stubRepo answers from a fixed map, recording what it was asked for.
type stubRepo struct {
	byHash map[string]auth.Identity
	asked  [][]byte
}

func (s *stubRepo) FindByHash(_ context.Context, hash []byte) (auth.Identity, error) {
	s.asked = append(s.asked, hash)
	identity, ok := s.byHash[string(hash)]
	if !ok {
		return auth.Identity{}, auth.ErrUnauthorized
	}
	return identity, nil
}

func TestAuthenticateResolvesALiveKey(t *testing.T) {
	t.Parallel()

	secret := "zt_live_goodsecret"
	want := auth.Identity{TenantID: uuid.New(), KeyID: uuid.New(), PlanID: "dev"}
	repo := &stubRepo{byHash: map[string]auth.Identity{string(auth.Hash(secret)): want}}

	got, err := auth.NewAuthenticator(repo).Authenticate(context.Background(), secret)

	require.NoError(t, err)
	require.Equal(t, want.TenantID, got.TenantID)
	require.Equal(t, auth.Hash(secret), repo.asked[0], "lookup is by digest, never by secret")
}

func TestAuthenticateRejectsUnknownAndMalformedAlike(t *testing.T) {
	t.Parallel()

	repo := &stubRepo{byHash: map[string]auth.Identity{}}
	authenticator := auth.NewAuthenticator(repo)

	for name, secret := range map[string]string{
		"unknown key":    "zt_live_nosuchkey",
		"foreign prefix": "sk_live_whatever",
		"empty":          "",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := authenticator.Authenticate(context.Background(), secret)

			require.ErrorIs(t, err, auth.ErrUnauthorized,
				"a prober must not be able to tell these apart")
		})
	}
}

func TestAuthenticateDoesNotQueryForAMalformedSecret(t *testing.T) {
	t.Parallel()

	repo := &stubRepo{byHash: map[string]auth.Identity{}}

	_, err := auth.NewAuthenticator(repo).Authenticate(context.Background(), "garbage")

	require.ErrorIs(t, err, auth.ErrUnauthorized)
	require.Empty(t, repo.asked, "a malformed credential must not reach the database")
}

func TestScopeChecks(t *testing.T) {
	t.Parallel()

	identity := auth.Identity{Scopes: []auth.Scope{auth.ScopeSynth, auth.ScopeUsage}}

	require.True(t, identity.Has(auth.ScopeSynth))
	require.False(t, identity.Has(auth.ScopeKeys))
	require.NoError(t, identity.Require(auth.ScopeUsage))

	err := identity.Require(auth.ScopeJobs)

	require.ErrorIs(t, err, auth.ErrScopeMissing)
	require.Contains(t, err.Error(), "jobs")
}

func TestDefaultTenantScopesMatchThePermissionsDoc(t *testing.T) {
	t.Parallel()

	require.ElementsMatch(t,
		[]auth.Scope{auth.ScopeSynth, auth.ScopeJobs, auth.ScopeUsage, auth.ScopeKeys},
		auth.DefaultTenantScopes)
}
