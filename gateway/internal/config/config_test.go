package config_test

import (
	"net/netip"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pcaokhai/vitts/gateway/internal/config"
)

// baseEnv is the minimum that lets Load succeed; tests override one key at a time.
var baseEnv = map[string]string{
	"VITTS_DATABASE_URL":           "postgres://vitts:vitts@localhost:5432/vitts?sslmode=disable",
	"VITTS_ADMIN_KEY":              strings.Repeat("k", 32),
	"VITTS_ADMIN_IP_ALLOWLIST":     "127.0.0.1/32",
	"VITTS_WORKER_ADDRS":           "worker:50051",
	"VITTS_REDIS_URL":              "redis://localhost:6379/0",
	"VITTS_S3_BUCKET":              "vitts",
	"VITTS_WEBHOOK_SIGNING_SECRET": strings.Repeat("w", 32),
}

func withBase(overrides map[string]string) map[string]string {
	merged := make(map[string]string, len(baseEnv)+len(overrides))
	for k, v := range baseEnv {
		merged[k] = v
	}
	for k, v := range overrides {
		merged[k] = v
	}
	return merged
}

func env(pairs map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		v, ok := pairs[name]
		return v, ok
	}
}

func TestLoadDefaults(t *testing.T) {
	t.Parallel()

	cfg, err := config.Load(env(withBase(nil)))

	require.NoError(t, err)
	require.Equal(t, config.EnvDev, cfg.Env)
	require.Positive(t, cfg.DatabaseMaxConns, "the pool must be bounded")
	require.Equal(t, ":8080", cfg.HTTPAddr)
	require.Equal(t, "info", cfg.LogLevel)
	require.Empty(t, cfg.OTLPEndpoint)
	require.Positive(t, cfg.ShutdownTimeout)
}

func TestLoadRejectsBadValues(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		vars     map[string]string
		variable string
	}{
		"unknown environment": {
			vars:     withBase(map[string]string{"VITTS_ENV": "staging"}),
			variable: "VITTS_ENV",
		},
		"unknown log level": {
			vars:     withBase(map[string]string{"VITTS_LOG_LEVEL": "verbose"}),
			variable: "VITTS_LOG_LEVEL",
		},
		"missing database url": {
			vars:     map[string]string{},
			variable: "VITTS_DATABASE_URL",
		},
		"short admin key": {
			vars:     withBase(map[string]string{"VITTS_ADMIN_KEY": "too-short"}),
			variable: "VITTS_ADMIN_KEY",
		},
		"empty admin allowlist": {
			vars:     withBase(map[string]string{"VITTS_ADMIN_IP_ALLOWLIST": ""}),
			variable: "VITTS_ADMIN_IP_ALLOWLIST",
		},
		"short webhook secret": {
			vars:     withBase(map[string]string{"VITTS_WEBHOOK_SIGNING_SECRET": "short"}),
			variable: "VITTS_WEBHOOK_SIGNING_SECRET",
		},
		"missing redis url": {
			vars:     withBase(map[string]string{"VITTS_REDIS_URL": ""}),
			variable: "VITTS_REDIS_URL",
		},
		"missing s3 bucket": {
			vars:     withBase(map[string]string{"VITTS_S3_BUCKET": ""}),
			variable: "VITTS_S3_BUCKET",
		},
		"missing worker addresses": {
			vars:     withBase(map[string]string{"VITTS_WORKER_ADDRS": ""}),
			variable: "VITTS_WORKER_ADDRS",
		},
		"unparseable admin allowlist": {
			vars:     withBase(map[string]string{"VITTS_ADMIN_IP_ALLOWLIST": "not-an-ip"}),
			variable: "VITTS_ADMIN_IP_ALLOWLIST",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := config.Load(env(tc.vars))

			var cfgErr *config.Error
			require.ErrorAs(t, err, &cfgErr)
			require.Equal(t, tc.variable, cfgErr.Variable)
			require.Contains(t, err.Error(), tc.variable)
		})
	}
}

func TestLoadTreatsBlankAsUnset(t *testing.T) {
	t.Parallel()

	cfg, err := config.Load(env(withBase(map[string]string{"VITTS_HTTP_ADDR": "   "})))

	require.NoError(t, err)
	require.Equal(t, ":8080", cfg.HTTPAddr)
}

func TestLoadNormalisesLogLevelCase(t *testing.T) {
	t.Parallel()

	cfg, err := config.Load(env(withBase(map[string]string{"VITTS_LOG_LEVEL": "DEBUG"})))

	require.NoError(t, err)
	require.Equal(t, "debug", cfg.LogLevel)
}

func TestAdminAllowlistAcceptsCIDRsAndBareAddresses(t *testing.T) {
	t.Parallel()

	cfg, err := config.Load(env(withBase(map[string]string{
		"VITTS_ADMIN_IP_ALLOWLIST": "10.0.0.0/8, 127.0.0.1 ,::1",
	})))

	require.NoError(t, err)
	require.Equal(t, []netip.Prefix{
		netip.MustParsePrefix("10.0.0.0/8"),
		netip.MustParsePrefix("127.0.0.1/32"),
		netip.MustParsePrefix("::1/128"),
	}, cfg.AdminAllowlist)
}

func TestWorkerAddressesAreSplitAndTrimmed(t *testing.T) {
	t.Parallel()

	cfg, err := config.Load(env(withBase(map[string]string{
		"VITTS_WORKER_ADDRS": " w1:50051 , w2:50051, ",
	})))

	require.NoError(t, err)
	require.Equal(t, []string{"w1:50051", "w2:50051"}, cfg.WorkerAddrs)
}
