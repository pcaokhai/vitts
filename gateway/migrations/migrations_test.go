package migrations_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pcaokhai/vitts/gateway/migrations"
)

// T-107. Latest must track the files actually embedded, so adding a migration without
// touching Go code still tightens the boot check.
func TestLatestIsTheHighestEmbeddedVersion(t *testing.T) {
	latest, err := migrations.Latest()
	require.NoError(t, err)

	entries, err := migrations.FS.ReadDir(".")
	require.NoError(t, err)
	require.Len(t, entries, int(latest), "versions are expected to be contiguous from 1")
	require.Positive(t, latest)
}
