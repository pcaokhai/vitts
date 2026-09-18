---
paths: ["gateway/**/*.go"]
---
# Go gateway rules

- Layering: `internal/http` → use cases (`synth`, `jobs`, `usage`, `voices`) → ports →
  adapters (`storage`, `dispatch`, `telemetry`). Use cases must not import `net/http`,
  `database/sql`, `github.com/redis/*`. Enforce with `depguard` in golangci config.
- Every exported function on a use case or adapter takes `ctx context.Context` first.
- No package-level mutable state. Wire dependencies in `cmd/gateway/main.go` only.
- Errors: define sentinel/typed errors per package; wrap with `fmt.Errorf("...: %w", err)`;
  map to HTTP once in `internal/http/problem.go`.
- Logging: `zerolog` with typed fields (`.Str("tenant_id", id)`); never log request
  bodies, headers, or errors that may embed user text without redaction.
- SQL through `sqlc` queries in `internal/storage/postgres/queries/*.sql`; every query on
  a tenant-owned table has `tenant_id = $n`.
- Redis atomic ops as Lua scripts under `internal/ratelimit/lua/`; load with `SCRIPT LOAD`
  at boot.
- gRPC client calls: `grpc.WaitForReady(false)`, deadline from caller ctx, interceptors
  for otel + metrics.
- Concurrency: use `errgroup` with limits, `semaphore.Weighted`, bounded channels; every
  goroutine has an owner and a way to stop.
- HTTP handlers: parse → validate → call use case → write response. Max 40 lines.
- Tests: table-driven, `testify/require`; integration tests `//go:build integration` using
  `testcontainers-go`; fake worker in `internal/dispatch/fakeworker` implements
  `workerpb.WorkerServer`.
- Run `golangci-lint run ./...` and `go test ./...` before reporting completion.
