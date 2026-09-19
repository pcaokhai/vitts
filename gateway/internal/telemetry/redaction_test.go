package telemetry_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pcaokhai/vitts/gateway/internal/telemetry"
)

// These are the shapes that must never appear in a log line (NFR-07, ADR-008).
const (
	apiKeySecret = "zt_live_thisisapretendsecretvalue"
	requestText  = "Xin chào, đây là nội dung riêng tư của khách hàng."
	authHeader   = "Bearer " + apiKeySecret
)

// T-11: the logger must carry metadata, never credentials or user text.
//
// This test is the guard on a property that code review alone cannot hold: it fails the
// moment anyone logs a field that happens to contain either.
func TestLoggerCarriesOnlyMetadata(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	logger := telemetry.NewLogger(&out, "info", false)

	logger.Info().
		Str("tenant_id", "6f1c0a2e-0000-4000-8000-000000000000").
		Str("key_id", "9d2b0a2e-0000-4000-8000-000000000000").
		Str("key_fingerprint", "aabbccddeeff").
		Str("voice_id", "maichi").
		Int("chars", len([]rune(requestText))).
		Msg("request")

	line := out.String()

	require.Contains(t, line, "maichi", "metadata is expected to be there")
	require.Contains(t, line, "aabbccddeeff", "a digest prefix is safe to log")
	require.NotContains(t, line, apiKeySecret)
	require.NotContains(t, line, requestText)
	require.NotContains(t, line, authHeader)
}

func TestLoggerLevelFiltersDebugDetail(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	logger := telemetry.NewLogger(&out, "warn", false)

	logger.Debug().Str("text", requestText).Msg("this must not be emitted")

	require.Empty(t, out.String(), "a level filter is the last line of defence")
}

func TestLoggerFallsBackToInfoOnAnUnknownLevel(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	logger := telemetry.NewLogger(&out, "nonsense", false)

	logger.Info().Msg("still logged")

	require.Contains(t, out.String(), "still logged",
		"a bad level must not silently disable logging")
}

func TestLoggerEmitsJSONForIngestion(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	logger := telemetry.NewLogger(&out, "info", false)

	logger.Info().Str("event", "request").Msg("done")

	line := strings.TrimSpace(out.String())
	require.True(t, strings.HasPrefix(line, "{") && strings.HasSuffix(line, "}"))
	require.Contains(t, line, `"level":"info"`)
	require.Contains(t, line, `"time":`)
}
