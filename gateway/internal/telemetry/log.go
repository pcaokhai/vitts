// Package telemetry wires logging and tracing. It is an adapter: nothing here knows what
// the gateway does, and no other package configures a logger or a tracer provider.
package telemetry

import (
	"io"
	"os"
	"time"

	"github.com/rs/zerolog"
)

// NewLogger builds the process logger. Production emits JSON for ingestion; dev emits a
// readable console stream. Level is already validated by config.
//
// Fields are typed by every caller: never log a request body, an Authorization header, or
// user text (ADR-008, .claude/rules/security.md).
func NewLogger(out io.Writer, level string, pretty bool) zerolog.Logger {
	parsed, err := zerolog.ParseLevel(level)
	if err != nil {
		parsed = zerolog.InfoLevel
	}

	if pretty {
		out = zerolog.ConsoleWriter{Out: out, TimeFormat: time.RFC3339}
	}

	return zerolog.New(out).Level(parsed).With().Timestamp().Logger()
}

// NewOSLogger is NewLogger against stdout.
func NewOSLogger(level string, pretty bool) zerolog.Logger {
	return NewLogger(os.Stdout, level, pretty)
}
