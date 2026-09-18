//go:build integration

// Package pgtest starts a throwaway PostgreSQL for integration tests and applies the
// real migrations to it, so tests run against the schema that ships.
package pgtest

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	_ "github.com/jackc/pgx/v5/stdlib" // database/sql driver, for goose only
)

const startupTimeout = 60 * time.Second

// Start returns a migrated database URL. The container is terminated when the test ends.
func Start(t *testing.T) string {
	t.Helper()

	ctx := context.Background()
	container, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("vitts"),
		tcpostgres.WithUsername("vitts"),
		tcpostgres.WithPassword("vitts"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(startupTimeout),
		),
	)
	require.NoError(t, err, "start postgres container")
	t.Cleanup(func() {
		if err := testcontainers.TerminateContainer(container); err != nil {
			t.Logf("terminate postgres container: %v", err)
		}
	})

	url, err := container.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)

	Migrate(t, url)
	return url
}

// Migrate applies every migration in gateway/migrations to url.
func Migrate(t *testing.T, url string) {
	t.Helper()

	sqlDB, err := sql.Open("pgx", url)
	require.NoError(t, err)
	defer func() { require.NoError(t, sqlDB.Close()) }()

	require.NoError(t, goose.SetDialect("postgres"))
	require.NoError(t, goose.Up(sqlDB, MigrationsDir(t)))
}

// MigrationsDir locates gateway/migrations relative to this source file, so tests do not
// depend on the working directory they are run from.
func MigrationsDir(t *testing.T) string {
	t.Helper()

	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok, "resolve caller")
	dir, err := filepath.Abs(filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "..", "migrations"))
	require.NoError(t, err, fmt.Sprintf("resolve migrations dir from %s", thisFile))
	return dir
}
