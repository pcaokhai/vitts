package config_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pcaokhai/vitts/gateway/internal/config"
)

func env(pairs map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		v, ok := pairs[name]
		return v, ok
	}
}

func TestLoadDefaults(t *testing.T) {
	t.Parallel()

	cfg, err := config.Load(env(nil))

	require.NoError(t, err)
	require.Equal(t, config.EnvDev, cfg.Env)
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
			vars:     map[string]string{"VITTS_ENV": "staging"},
			variable: "VITTS_ENV",
		},
		"unknown log level": {
			vars:     map[string]string{"VITTS_LOG_LEVEL": "verbose"},
			variable: "VITTS_LOG_LEVEL",
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

	cfg, err := config.Load(env(map[string]string{"VITTS_HTTP_ADDR": "   "}))

	require.NoError(t, err)
	require.Equal(t, ":8080", cfg.HTTPAddr)
}

func TestLoadNormalisesLogLevelCase(t *testing.T) {
	t.Parallel()

	cfg, err := config.Load(env(map[string]string{"VITTS_LOG_LEVEL": "DEBUG"}))

	require.NoError(t, err)
	require.Equal(t, "debug", cfg.LogLevel)
}
