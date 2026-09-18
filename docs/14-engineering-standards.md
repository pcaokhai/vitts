# Engineering standards

These are the rules every PR is reviewed against. `CLAUDE.md` summarises them for the
agent; this file is the authority.

## Architecture rules

1. Hexagonal layering in the gateway: `http` (adapters in) → use cases (`synth`, `jobs`,
   …) → ports (interfaces) → `storage`/`dispatch` (adapters out). Use cases never import
   `net/http` or SQL. Adapters never contain business rules.
2. Every external call (Postgres, Redis, S3, gRPC, webhook) has a context deadline and is
   wrapped for metrics and tracing.
3. Contracts first: change `openapi.yaml` / `worker.proto`, regenerate, then implement.
4. One writer per aggregate: only `jobs` package mutates `jobs`/`job_segments`.
5. Errors are values with a stable `code`; wrap with `%w`; map to HTTP in one place.
6. Concurrency: bounded everything (queues, worker pools, batch sizes). No unbounded
   goroutine spawn per request without a semaphore.
7. Idempotency for every side effect that can be retried (usage insert, job create,
   webhook).
8. No feature without a metric and a test.

## Go

- Go 1.23, modules, `golangci-lint` with `errcheck, govet, staticcheck, gosec, revive,
  gocritic, errorlint, contextcheck, bodyclose, sqlclosecheck`.
- Package layout `internal/<domain>`; exported surface minimal; no `util`/`common`.
- Constructor injection; no globals; `context.Context` first param.
- Table-driven tests; `t.Parallel()` where safe; integration tests tagged `//go:build integration`.
- SQL via `sqlc`; no string-built queries; transactions via a `TxRunner` port.
- Logging: `zerolog`, typed fields only; never `Interface("req", req)`.
- Return `problem+json` through one `writeProblem` helper.

## Python

- Python 3.12, `uv` with lockfile, `ruff` (lint + format), `mypy --strict`, `pytest` with
  `pytest-asyncio`.
- Async gRPC servicer (`grpc.aio`); inference runs in a thread executor guarded by a lock
  per process; check cancellation between frames.
- No global mutable state except the engine singleton created in `main`.
- Structured logging via `structlog` JSON; no text in logs.

## API design

- REST nouns, plural; versioned under `/v1`; breaking change → `/v2`.
- Problem+json with stable `code`; `X-Request-Id` on every response; pagination by cursor.
- Streaming endpoints document exact byte framing.
- Defaults and bounds live in the OpenAPI schema and are enforced by generated validators.

## Data

- Migrations forward-only, expand/contract for changes; every migration reversible in
  the sense that the previous release keeps working.
- Every tenant-owned table has `tenant_id`; every query includes it.
- Time in UTC in storage; ICT only at presentation.

## Security

- Secrets only from env/secret store; `gitleaks` in CI.
- Constant-time comparisons; keys stored as sha256; admin allowlist.
- Input limits everywhere: body 1 MiB, text 3,000/100,000, metadata 10 keys.
- SSRF guard on outbound URLs; TLS ≥ 1.2; security headers on console.
- Dependencies pinned; `govulncheck`, `pip-audit` weekly; base images by digest, non-root.

## Git and review

- Trunk-based: short-lived branches `feat/…`, `fix/…`; squash merge; conventional
  commits (`feat(gateway): …`).
- PR template: what, why, how tested, docs updated, ADR touched?
- CI must be green; at least one review (self-review checklist for solo work).
- No commit of generated code without regeneration check; no `TODO` without an issue link.

## Definition of done

Code, tests (unit + integration where a flow is touched), docs (`docs/` and OpenAPI),
metrics, and a green CI run. Anything that changes an ADR decision requires a new ADR.
