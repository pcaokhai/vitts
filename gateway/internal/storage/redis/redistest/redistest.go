//go:build integration

// Package redistest starts a throwaway Redis for integration tests.
package redistest

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"
)

const startupTimeout = 60 * time.Second

// Start returns a Redis URL. The container is terminated when the test ends.
func Start(t *testing.T) string {
	t.Helper()

	ctx := context.Background()
	container, err := tcredis.Run(ctx, "redis:7-alpine")
	require.NoError(t, err, "start redis container")
	t.Cleanup(func() {
		if err := testcontainers.TerminateContainer(container); err != nil {
			t.Logf("terminate redis container: %v", err)
		}
	})

	ctx, cancel := context.WithTimeout(ctx, startupTimeout)
	defer cancel()

	url, err := container.ConnectionString(ctx)
	require.NoError(t, err)
	return url
}
