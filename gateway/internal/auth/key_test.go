package auth_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pcaokhai/vitts/gateway/internal/auth"
)

func TestGenerateProducesAUsableKey(t *testing.T) {
	t.Parallel()

	key, err := auth.Generate(auth.PrefixLive)

	require.NoError(t, err)
	require.True(t, strings.HasPrefix(key.Secret, auth.PrefixLive))
	require.Len(t, key.Hash, 32, "the schema constrains key_hash to 32 bytes")
	require.Equal(t, auth.Hash(key.Secret), key.Hash)
	require.True(t, strings.HasPrefix(key.Prefix, auth.PrefixLive))
	require.NotContains(t, key.Secret[len(auth.PrefixLive):], key.Prefix[len(auth.PrefixLive):]+"=")
}

func TestGenerateIsUnique(t *testing.T) {
	t.Parallel()

	seen := make(map[string]struct{}, 100)
	for range 100 {
		key, err := auth.Generate(auth.PrefixTest)
		require.NoError(t, err)
		require.NotContains(t, seen, key.Secret)
		seen[key.Secret] = struct{}{}
	}
}

func TestGenerateRejectsUnknownPrefix(t *testing.T) {
	t.Parallel()

	_, err := auth.Generate("sk_live_")

	require.ErrorIs(t, err, auth.ErrMalformedKey)
}

func TestStoredPrefixIsNotACredential(t *testing.T) {
	t.Parallel()

	key, err := auth.Generate(auth.PrefixLive)
	require.NoError(t, err)

	require.Less(t, len(key.Prefix), len(key.Secret))
	require.NotEqual(t, key.Secret, key.Prefix)
}

func TestParseBearer(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		header  string
		want    string
		wantErr bool
	}{
		"live key":          {header: "Bearer zt_live_abc123", want: "zt_live_abc123"},
		"test key":          {header: "Bearer zt_test_abc123", want: "zt_test_abc123"},
		"lowercase scheme":  {header: "bearer zt_live_abc123", want: "zt_live_abc123"},
		"surrounding space": {header: "Bearer   zt_live_abc123  ", want: "zt_live_abc123"},
		"empty":             {header: "", wantErr: true},
		"no scheme":         {header: "zt_live_abc123", wantErr: true},
		"basic auth":        {header: "Basic dXNlcjpwYXNz", wantErr: true},
		"foreign prefix":    {header: "Bearer sk_live_abc123", wantErr: true},
		"prefix only":       {header: "Bearer zt_live_", wantErr: true},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, err := auth.ParseBearer(tc.header)

			if tc.wantErr {
				require.ErrorIs(t, err, auth.ErrMalformedKey)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestEqualHashIsExact(t *testing.T) {
	t.Parallel()

	a := auth.Hash("zt_live_one")
	b := auth.Hash("zt_live_two")

	require.True(t, auth.EqualHash(a, auth.Hash("zt_live_one")))
	require.False(t, auth.EqualHash(a, b))
	require.False(t, auth.EqualHash(a, a[:16]))
}

func TestFingerprintRevealsOnlyAPrefixOfTheDigest(t *testing.T) {
	t.Parallel()

	key, err := auth.Generate(auth.PrefixLive)
	require.NoError(t, err)

	fingerprint := auth.FingerprintForLogs(key.Hash)

	require.Len(t, fingerprint, 12)
	require.NotContains(t, key.Secret, fingerprint)
}
