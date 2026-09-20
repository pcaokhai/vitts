// Command gateway is the ViTTS API gateway.
//
// This file is the only place dependencies are constructed and wired; every other package
// receives what it needs through its constructor (.claude/rules/gateway-go.md).
package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	stdhttp "net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // database/sql driver, used by the migrations job
	"github.com/pressly/goose/v3"
	"github.com/rs/zerolog"

	"github.com/pcaokhai/vitts/gateway/internal/auth"
	"github.com/pcaokhai/vitts/gateway/internal/cache"
	"github.com/pcaokhai/vitts/gateway/internal/config"
	"github.com/pcaokhai/vitts/gateway/internal/dispatch"
	gatewayhttp "github.com/pcaokhai/vitts/gateway/internal/http"
	"github.com/pcaokhai/vitts/gateway/internal/jobs"
	"github.com/pcaokhai/vitts/gateway/internal/plans"
	"github.com/pcaokhai/vitts/gateway/internal/quota"
	"github.com/pcaokhai/vitts/gateway/internal/ratelimit"
	"github.com/pcaokhai/vitts/gateway/internal/storage/postgres"
	redisadapter "github.com/pcaokhai/vitts/gateway/internal/storage/redis"
	"github.com/pcaokhai/vitts/gateway/internal/storage/s3"
	"github.com/pcaokhai/vitts/gateway/internal/synth"
	"github.com/pcaokhai/vitts/gateway/internal/telemetry"
	"github.com/pcaokhai/vitts/gateway/internal/tenants"
	"github.com/pcaokhai/vitts/gateway/internal/usage"
	"github.com/pcaokhai/vitts/gateway/internal/voices"
	"github.com/pcaokhai/vitts/gateway/migrations"
)

// readHeaderTimeout bounds the header phase so a slow-loris client cannot hold a
// connection open for free.
const readHeaderTimeout = 10 * time.Second

// healthcheckTimeout bounds the self-probe used as the container health check.
const healthcheckTimeout = 3 * time.Second

// seedTimeout bounds the plan upsert; it touches a handful of rows.
const seedTimeout = 30 * time.Second

func main() {
	// The image is distroless: no shell, no curl. The binary probes itself instead.
	healthcheck := flag.Bool("healthcheck", false, "probe /healthz on the configured address and exit")
	migrate := flag.Bool("migrate", false, "apply database migrations and exit")
	seedPlans := flag.String("seed-plans", "", "upsert plan tiers from this YAML file and exit")
	flag.Parse()

	if *seedPlans != "" {
		if err := runSeedPlans(*seedPlans); err != nil {
			fmt.Fprintf(os.Stderr, "gateway: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if *migrate {
		if err := runMigrations(); err != nil {
			fmt.Fprintf(os.Stderr, "gateway: %v\n", err)
			os.Exit(1)
		}
		return
	}

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

	pool, err := postgres.Open(ctx, cfg.DatabaseURL, cfg.DatabaseMaxConns)
	if err != nil {
		return fmt.Errorf("postgres: %w", err)
	}
	defer pool.Close()

	cacheClient, err := redisadapter.Open(ctx, cfg.RedisURL, cfg.RedisPoolSize)
	if err != nil {
		return fmt.Errorf("redis: %w", err)
	}
	defer func() {
		if err := cacheClient.Close(); err != nil {
			logger.Error().Err(err).Msg("redis shutdown incomplete")
		}
	}()

	limiter, err := ratelimit.New(ctx, cacheClient.Raw())
	if err != nil {
		return fmt.Errorf("rate limiter: %w", err)
	}

	quotas := quota.New(redisadapter.NewCounter(cacheClient))
	// Reconcile runs for the life of the process; ctx is cancelled on shutdown.
	go quota.NewReconciler(quotas, postgres.NewUsageRepository(pool), logger).Run(ctx)

	workers, err := dispatch.NewPool(cfg.WorkerAddrs, logger, dispatch.Options{})
	if err != nil {
		return fmt.Errorf("worker pool: %w", err)
	}
	defer func() {
		if err := workers.Close(); err != nil {
			logger.Error().Err(err).Msg("worker pool shutdown incomplete")
		}
	}()
	workers.Start(ctx)

	catalogue := voices.NewService(postgres.NewVoiceRepository(pool), workers)
	planLimits := postgres.NewPlanLimits(pool)
	cacheManager := cache.NewManager(
		redisadapter.NewCacheIndex(cacheClient),
		s3.Open(cfg.S3),
		postgres.NewCacheCatalogue(pool),
	)
	meter := usage.New(postgres.NewUsageWriter(pool), logger)
	meter.Start()
	defer meter.Stop()

	dispatcher := dispatch.NewDispatcher(workers)
	synthesizer := synth.NewService(
		synth.NewQuotaAdapter(quotas),
		cacheManager,
		dispatcher,
		catalogue,
		planLimits,
		synth.NewMeterAdapter(meter),
		logger,
	)
	leases := synth.NewLeaseAdapter(limiter)

	objects := s3.Open(cfg.S3)
	jobTexts := s3.NewJobTexts(objects)
	jobQueue := redisadapter.NewJobQueue(cacheClient)
	if err := jobQueue.EnsureGroups(ctx); err != nil {
		return fmt.Errorf("job queue: %w", err)
	}
	jobRepo := postgres.NewJobRepository(pool)
	jobService := jobs.NewService(jobRepo, jobQueue, jobTexts, jobs.NewSSRFGuard(), logger)

	// Jobs run at ClassBatch, so an interactive stream always outranks them (ADR-007).
	jobEngine := jobs.NewWorkerEngine(dispatcher, jobTexts, cacheManager, logger)
	orchestrator := jobs.NewOrchestrator(
		jobQueue, jobRepo, jobTexts, jobEngine, nil, consumerName(cfg), logger,
	)
	orchestrator.Run(ctx)

	// The catalogue follows the fleet: a deploy that changes the model's voices must
	// show up without a migration (US-13).
	go syncVoices(ctx, catalogue, logger)

	readiness := gatewayhttp.NewReadiness()
	readiness.Register("postgres", pool.Ready)
	readiness.Register("redis", cacheClient.Ready)
	readiness.Register("workers", workers.Ready)

	server := &stdhttp.Server{
		Addr: cfg.HTTPAddr,
		Handler: gatewayhttp.Router(gatewayhttp.Deps{
			Logger:    logger,
			Readiness: readiness,
			Admin:     gatewayhttp.NewAdminGuard(cfg.AdminKey, cfg.AdminAllowlist),
			Tenants:   tenants.NewService(postgres.NewTenantRepository(pool)),
			Auth:      auth.NewAuthenticator(postgres.NewAuthRepository(pool)),
			Limiter:   limiter,
			Plans:     planLimits,
			TenantAPI: []gatewayhttp.Route{
				{
					Method: stdhttp.MethodPost, Pattern: "/synthesize",
					Scope: auth.ScopeSynth, Handler: gatewayhttp.Synthesize(synthesizer),
				},
				{
					Method: stdhttp.MethodPost, Pattern: "/synthesize/stream",
					Scope:   auth.ScopeSynth,
					Handler: gatewayhttp.SynthesizeStream(synthesizer, leases, planLimits),
				},
				{
					Method: stdhttp.MethodGet, Pattern: "/synthesize/ws",
					Scope:   auth.ScopeSynth,
					Handler: gatewayhttp.SynthesizeWS(synthesizer, leases, planLimits),
				},
				{
					Method: stdhttp.MethodGet, Pattern: "/voices",
					Scope: auth.ScopeSynth, Handler: gatewayhttp.ListVoices(catalogue),
				},
				{
					Method: stdhttp.MethodPost, Pattern: "/jobs",
					Scope: auth.ScopeJobs, Handler: gatewayhttp.CreateJob(jobService, planLimits),
				},
				{
					Method: stdhttp.MethodGet, Pattern: "/jobs",
					Scope: auth.ScopeJobs, Handler: gatewayhttp.ListJobs(jobService),
				},
				{
					Method: stdhttp.MethodGet, Pattern: "/jobs/{jobId}",
					Scope: auth.ScopeJobs, Handler: gatewayhttp.GetJob(jobService),
				},
				{
					Method: stdhttp.MethodDelete, Pattern: "/jobs/{jobId}",
					Scope: auth.ScopeJobs, Handler: gatewayhttp.CancelJob(jobService),
				},
			},
		}),
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
	// Deliberately not the full config: a health probe must keep working even when the
	// serving configuration is incomplete, so it reads only the address it must reach.
	addr := os.Getenv("VITTS_HTTP_ADDR")
	if addr == "" {
		addr = ":8080"
	}

	ctx, cancel := context.WithTimeout(context.Background(), healthcheckTimeout)
	defer cancel()

	req, err := stdhttp.NewRequestWithContext(ctx, stdhttp.MethodGet, healthURL(addr), nil)
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

// runMigrations applies the embedded migrations. This is the deploy's migrations job
// (docs/13-runbook.md); the serving process never migrates on startup, so several
// replicas can roll without racing each other over the schema.
func runMigrations() error {
	// Only the database URL: a migrations job has no business holding the admin key or
	// any other serving credential.
	url := os.Getenv("VITTS_DATABASE_URL")
	if url == "" {
		return errors.New("VITTS_DATABASE_URL is required")
	}

	sqlDB, err := sql.Open("pgx", url)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer func() { _ = sqlDB.Close() }()

	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("goose dialect: %w", err)
	}
	if err := goose.Up(sqlDB, "."); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
}

// runSeedPlans upserts the operator's plan file. Separate from the migrations job because
// plan figures are configuration that changes without a schema change, and re-running it
// is how a limit is adjusted.
func runSeedPlans(path string) error {
	url := os.Getenv("VITTS_DATABASE_URL")
	if url == "" {
		return errors.New("VITTS_DATABASE_URL is required")
	}

	tiers, err := plans.Load(path)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), seedTimeout)
	defer cancel()

	pool, err := postgres.Open(ctx, url, 2)
	if err != nil {
		return fmt.Errorf("postgres: %w", err)
	}
	defer pool.Close()

	if err := postgres.UpsertPlans(ctx, pool, tiers); err != nil {
		return fmt.Errorf("upsert plans: %w", err)
	}

	fmt.Printf("upserted %d plans from %s\n", len(tiers), path)
	return nil
}

// voiceSyncInterval is how often the catalogue is refreshed from the fleet. Voices change
// at deploy time, so this only has to be faster than an operator noticing.
const voiceSyncInterval = time.Minute

// syncVoices keeps the catalogue in step with what the workers advertise.
func syncVoices(ctx context.Context, catalogue *voices.Service, logger zerolog.Logger) {
	ticker := time.NewTicker(voiceSyncInterval)
	defer ticker.Stop()

	for {
		if err := catalogue.Sync(ctx); err != nil && ctx.Err() == nil {
			logger.Error().Err(err).Msg("voice catalogue sync failed")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// consumerName identifies this replica inside the Redis consumer group. A stable,
// unique name is what lets the reconciler tell "my own pending work" from "a dead
// replica's" (FL-03).
func consumerName(cfg config.Config) string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "gateway"
	}
	return fmt.Sprintf("%s-%s-%d", host, cfg.Env, os.Getpid())
}
