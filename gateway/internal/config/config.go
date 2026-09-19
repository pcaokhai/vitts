// Package config loads and validates the gateway's environment once at boot.
//
// A value that is wrong here must stop the process with the offending variable named,
// never surface later as a failed request (docs/08-variables.md).
package config

import (
	"fmt"
	"net/netip"
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
	Env              Env
	HTTPAddr         string
	LogLevel         string
	OTLPEndpoint     string // empty disables tracing export
	DatabaseURL      string
	DatabaseMaxConns int32
	AdminKey         string
	AdminAllowlist   []netip.Prefix
	WorkerAddrs      []string
	ShutdownTimeout  time.Duration
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
	// defaultDatabaseMaxConns keeps the pool bounded and well under Postgres'
	// default max_connections, which several gateway replicas share.
	defaultDatabaseMaxConns = 20
	// minAdminKeyLen matches the pre-go-live checklist in docs/08-variables.md.
	minAdminKeyLen = 32
)

var validLogLevels = map[string]struct{}{
	"trace": {}, "debug": {}, "info": {}, "warn": {}, "error": {},
}

// Load reads the environment via lookup, which is os.LookupEnv outside tests.
func Load(lookup func(string) (string, bool)) (Config, error) {
	cfg := Config{
		Env:              Env(value(lookup, "VITTS_ENV", string(EnvDev))),
		HTTPAddr:         value(lookup, "VITTS_HTTP_ADDR", defaultHTTPAddr),
		LogLevel:         strings.ToLower(value(lookup, "VITTS_LOG_LEVEL", defaultLogLevel)),
		OTLPEndpoint:     value(lookup, "OTEL_EXPORTER_OTLP_ENDPOINT", ""),
		DatabaseURL:      value(lookup, "VITTS_DATABASE_URL", ""),
		DatabaseMaxConns: defaultDatabaseMaxConns,
		AdminKey:         value(lookup, "VITTS_ADMIN_KEY", ""),
		WorkerAddrs:      splitList(value(lookup, "VITTS_WORKER_ADDRS", "")),
		ShutdownTimeout:  defaultShutdownTimeout,
	}

	allowlist, err := parsePrefixes(value(lookup, "VITTS_ADMIN_IP_ALLOWLIST", ""))
	if err != nil {
		return Config{}, &Error{Variable: "VITTS_ADMIN_IP_ALLOWLIST", Reason: err.Error()}
	}
	cfg.AdminAllowlist = allowlist

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
	if cfg.DatabaseURL == "" {
		return Config{}, &Error{
			Variable: "VITTS_DATABASE_URL",
			Reason:   "is required; the gateway does not run without its system of record",
		}
	}
	if _, err := url.Parse(cfg.DatabaseURL); err != nil {
		return Config{}, &Error{Variable: "VITTS_DATABASE_URL", Reason: "must be a URL"}
	}
	// The admin surface can create tenants and read across them, so a short key or an
	// empty allowlist is a boot failure, not a warning.
	if len(cfg.AdminKey) < minAdminKeyLen {
		return Config{}, &Error{
			Variable: "VITTS_ADMIN_KEY",
			Reason:   fmt.Sprintf("must be at least %d characters", minAdminKeyLen),
		}
	}
	if len(cfg.AdminAllowlist) == 0 {
		return Config{}, &Error{
			Variable: "VITTS_ADMIN_IP_ALLOWLIST",
			Reason:   "must list at least one CIDR; admin endpoints are never open",
		}
	}
	if len(cfg.WorkerAddrs) == 0 {
		return Config{}, &Error{
			Variable: "VITTS_WORKER_ADDRS",
			Reason:   "must list at least one worker address",
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

// parsePrefixes reads a comma-separated CIDR list. A bare address is accepted and treated
// as a single-host prefix, because "127.0.0.1" is what an operator types.
func parsePrefixes(raw string) ([]netip.Prefix, error) {
	if raw == "" {
		return nil, nil
	}

	parts := strings.Split(raw, ",")
	prefixes := make([]netip.Prefix, 0, len(parts))
	for _, part := range parts {
		entry := strings.TrimSpace(part)
		if entry == "" {
			continue
		}
		if prefix, err := netip.ParsePrefix(entry); err == nil {
			prefixes = append(prefixes, prefix)
			continue
		}
		addr, err := netip.ParseAddr(entry)
		if err != nil {
			return nil, fmt.Errorf("%q is not a CIDR or an IP address", entry)
		}
		prefixes = append(prefixes, netip.PrefixFrom(addr, addr.BitLen()))
	}
	return prefixes, nil
}

// splitList reads a comma-separated list, dropping empty entries so a trailing comma is
// not a configuration error.
func splitList(raw string) []string {
	if raw == "" {
		return nil
	}

	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if entry := strings.TrimSpace(part); entry != "" {
			out = append(out, entry)
		}
	}
	return out
}
