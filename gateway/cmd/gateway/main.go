// Command gateway is the ViTTS API gateway.
//
// This file is the only place dependencies are constructed and wired; every other package
// receives what it needs through its constructor (.claude/rules/gateway-go.md).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	stdhttp "net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/pcaokhai/vitts/gateway/internal/config"
	gatewayhttp "github.com/pcaokhai/vitts/gateway/internal/http"
	"github.com/pcaokhai/vitts/gateway/internal/telemetry"
)

// readHeaderTimeout bounds the header phase so a slow-loris client cannot hold a
// connection open for free.
const readHeaderTimeout = 10 * time.Second

// healthcheckTimeout bounds the self-probe used as the container health check.
const healthcheckTimeout = 3 * time.Second

func main() {
	// The image is distroless: no shell, no curl. The binary probes itself instead.
	healthcheck := flag.Bool("healthcheck", false, "probe /healthz on the configured address and exit")
	flag.Parse()

	if *healthcheck {
		if err := probe(); err != nil {
			fmt.Fprintf(os.Stderr, "unhealthy: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "gateway: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.LoadFromOS()
	if err != nil {
		return fmt.Errorf("configuration: %w", err)
	}

	logger := telemetry.NewOSLogger(cfg.LogLevel, cfg.Env == config.EnvDev)

	// Signals are trapped before anything long-running starts, so a Ctrl-C during
	// startup is honoured rather than swallowed.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	shutdownTracing, err := telemetry.Tracing(ctx, cfg.OTLPEndpoint)
	if err != nil {
		// A missing collector must not stop the gateway from serving traffic.
		logger.Error().Err(err).Msg("tracing disabled")
	}

	readiness := gatewayhttp.NewReadiness()
	server := &stdhttp.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           gatewayhttp.Router(logger, readiness),
		ReadHeaderTimeout: readHeaderTimeout,
	}

	serverErr := make(chan error, 1)
	go func() {
		logger.Info().
			Str("addr", cfg.HTTPAddr).
			Str("env", string(cfg.Env)).
			Bool("tracing", cfg.OTLPEndpoint != "").
			Msg("gateway listening")

		if err := server.ListenAndServe(); err != nil && !errors.Is(err, stdhttp.ErrServerClosed) {
			serverErr <- err
			return
		}
		serverErr <- nil
	}()

	select {
	case err := <-serverErr:
		if err != nil {
			return fmt.Errorf("listen: %w", err)
		}
	case <-ctx.Done():
		logger.Info().Dur("grace", cfg.ShutdownTimeout).Msg("shutdown started")
	}

	// Stop trapping signals: a second Ctrl-C during the drain should kill the process
	// rather than be swallowed by the handler.
	stop()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()

	var shutdownErr error
	if err := server.Shutdown(shutdownCtx); err != nil {
		shutdownErr = fmt.Errorf("http shutdown: %w", err)
		logger.Error().Err(err).Msg("http shutdown incomplete")
	}
	if err := shutdownTracing(shutdownCtx); err != nil {
		logger.Error().Err(err).Msg("tracing shutdown incomplete")
	}

	logger.Info().Msg("shutdown complete")
	return shutdownErr
}

// probe requests /healthz on the address this process would listen on.
func probe() error {
	cfg, err := config.LoadFromOS()
	if err != nil {
		return fmt.Errorf("configuration: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), healthcheckTimeout)
	defer cancel()

	req, err := stdhttp.NewRequestWithContext(ctx, stdhttp.MethodGet, healthURL(cfg.HTTPAddr), nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}

	res, err := stdhttp.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("probe: %w", err)
	}
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode != stdhttp.StatusOK {
		return fmt.Errorf("probe: status %d", res.StatusCode)
	}
	return nil
}

// healthURL turns a listen address into a loopback URL. A bare ":8080" listens on every
// interface, so the probe targets 127.0.0.1.
func healthURL(addr string) string {
	host, port, found := cutLast(addr, ":")
	if !found {
		host, port = "127.0.0.1", addr
	}
	if host == "" || host == "0.0.0.0" || host == "[::]" {
		host = "127.0.0.1"
	}
	return (&url.URL{Scheme: "http", Host: host + ":" + port, Path: "/healthz"}).String()
}

func cutLast(s, sep string) (before, after string, found bool) {
	for i := len(s) - len(sep); i >= 0; i-- {
		if s[i:i+len(sep)] == sep {
			return s[:i], s[i+len(sep):], true
		}
	}
	return s, "", false
}
