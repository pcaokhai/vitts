// Package config loads and validates the gateway's environment once at boot.
//
// A value that is wrong here must stop the process with the offending variable named,
// never surface later as a failed request (docs/08-variables.md).
package config

import (
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
)

// Env names the deployment environment. It decides log format and nothing else here.
type Env string

// The two environments the gateway recognises.
const (
	EnvDev  Env = "dev"
	EnvProd Env = "prod"
)

// Config is the whole configuration of the gateway process. Fields arrive with the task
// that needs them, so a missing variable always fails at the task that introduced it.
type Config struct {
	Env             Env
	HTTPAddr        string
	LogLevel        string
	OTLPEndpoint    string // empty disables tracing export
	ShutdownTimeout time.Duration
}

// Error reports a variable that is missing or unusable.
type Error struct {
	Variable string
	Reason   string
}

func (e *Error) Error() string {
	return fmt.Sprintf("%s: %s", e.Variable, e.Reason)
}

const (
	defaultHTTPAddr        = ":8080"
	defaultLogLevel        = "info"
	defaultShutdownTimeout = 20 * time.Second
)

var validLogLevels = map[string]struct{}{
	"trace": {}, "debug": {}, "info": {}, "warn": {}, "error": {},
}

// Load reads the environment via lookup, which is os.LookupEnv outside tests.
func Load(lookup func(string) (string, bool)) (Config, error) {
	cfg := Config{
		Env:             Env(value(lookup, "VITTS_ENV", string(EnvDev))),
		HTTPAddr:        value(lookup, "VITTS_HTTP_ADDR", defaultHTTPAddr),
		LogLevel:        strings.ToLower(value(lookup, "VITTS_LOG_LEVEL", defaultLogLevel)),
		OTLPEndpoint:    value(lookup, "OTEL_EXPORTER_OTLP_ENDPOINT", ""),
		ShutdownTimeout: defaultShutdownTimeout,
	}

	if cfg.Env != EnvDev && cfg.Env != EnvProd {
		return Config{}, &Error{Variable: "VITTS_ENV", Reason: `must be "dev" or "prod"`}
	}
	if cfg.HTTPAddr == "" {
		return Config{}, &Error{Variable: "VITTS_HTTP_ADDR", Reason: "must not be empty"}
	}
	if _, ok := validLogLevels[cfg.LogLevel]; !ok {
		return Config{}, &Error{
			Variable: "VITTS_LOG_LEVEL",
			Reason:   "must be one of trace, debug, info, warn, error",
		}
	}
	if cfg.OTLPEndpoint != "" {
		if _, err := url.Parse(cfg.OTLPEndpoint); err != nil {
			return Config{}, &Error{
				Variable: "OTEL_EXPORTER_OTLP_ENDPOINT",
				Reason:   "must be a URL",
			}
		}
	}

	return cfg, nil
}

// LoadFromOS is Load against the process environment.
func LoadFromOS() (Config, error) { return Load(os.LookupEnv) }

func value(lookup func(string) (string, bool), name, fallback string) string {
	if raw, ok := lookup(name); ok {
		if trimmed := strings.TrimSpace(raw); trimmed != "" {
			return trimmed
		}
	}
	return fallback
}
