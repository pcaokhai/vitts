# Architecture — ViTTS Gateway

## 1. Product overview and key assumptions

A Go API gateway fronts a pool of Python workers that run ZeroTTS on CPU. The gateway
owns everything that is not inference: auth, quotas, rate limits, cache, dispatch,
metering, jobs. Workers own inference and audio encoding only. All state lives in
PostgreSQL, Redis and S3-compatible object storage so both tiers are stateless and scale
independently.

Assumptions: one inference at a time per worker process (see ADR-003); text ≤ 3,000
chars for sync/stream, longer text must use jobs; Vietnamese is the only language
target, English code-switching is tolerated by the model.

## 2. Repository layout (monorepo)

```
vitts/
  CLAUDE.md                 agent operating instructions
  .claude/rules/            scoped rules loaded by Claude Code
  docs/                     this documentation set
  proto/                    worker.proto (single source of the gRPC contract)
  gateway/                  Go service
    cmd/gateway/            main
    internal/
      config/               env loading and validation
      http/                 handlers, middleware, DTOs (no business logic)
      auth/                 API key hashing, lookup, scopes
      ratelimit/            token bucket + concurrency semaphore (Redis Lua)
      quota/                monthly character quota
      cache/                cache key derivation, Redis index, S3 audio store
      dispatch/             worker pool, slot reservation, priority queue
      synth/                use case orchestration for sync/stream
      jobs/                 job state machine, segment fan-out, merge
      usage/                metering, batching, daily rollup
      voices/               catalogue
      storage/              postgres (sqlc), redis, s3 adapters
      telemetry/            otel, prometheus, zerolog
    migrations/             goose SQL migrations
    api/                    generated OpenAPI server stubs (oapi-codegen)
  worker/                   Python service
    vitts_worker/
      server.py             gRPC servicer
      engine.py             ZeroTTS wrapper, session lifecycle
      preprocess.py         normalize + chunk (thin wrapper over zerotts)
      encode.py             PCM16, Opus, MP3, resample
      health.py             readiness, slots, model version
    tests/
  deploy/
    docker-compose.yml      full local stack
    docker-compose.prod.yml
    grafana/, prometheus/
  scripts/                  bench, load test (k6), seed
```

## 3. C4 model

### Level 1 — Context

| Actor / system | Relationship |
|----------------|--------------|
| Tenant applications | Call REST/WebSocket API with API key |
| Operator | Manages tenants/plans via admin API and Grafana |
| Hugging Face Hub | Source of model weights and voice packs at worker boot (mirrored to S3 after first pull) |
| Object storage | Stores cached audio and job outputs |

### Level 2 — Containers

| Container | Tech | Responsibility | Scaling |
|-----------|------|----------------|---------|
| API gateway | Go 1.25, chi, grpc-go | Auth, limits, cache, dispatch, jobs, metering, admin | Horizontal, stateless, 2+ replicas behind LB |
| Worker | Python 3.12, onnxruntime, grpcio | Preprocess, inference, encode | Horizontal; one container = N processes; scale on queue depth |
| Redis 7 | | Rate limit, concurrency, quota counters, cache index, job stream, auth cache | Single node + persistence for MVP; Sentinel later |
| PostgreSQL 16 | | Tenants, keys, plans, voices, requests, jobs, usage | Single primary + daily backup; read replica later |
| Object storage | S3 / MinIO / R2 | Audio cache, job outputs, weight mirror | Managed |
| Observability | Prometheus, Grafana, OTel collector, Loki | Metrics, traces, logs | Self-hosted |

Gateway ↔ worker: gRPC server-streaming over private network, mTLS optional in MVP
(network isolation), required when workers leave the private network.

### Level 3 — Gateway components

| Component | Package | Depends on | Notes |
|-----------|---------|------------|-------|
| HTTP handlers | `internal/http` | synth, jobs, voices, usage use cases | Thin; validation and DTO mapping only |
| Auth | `internal/auth` | Redis, Postgres | Constant-time compare on hash; 60 s Redis cache; revocation invalidates |
| Rate limiter | `internal/ratelimit` | Redis Lua | Token bucket per key; semaphore per tenant with TTL lease |
| Quota | `internal/quota` | Redis, Postgres | Monthly counter; soft overrun 5% |
| Cache manager | `internal/cache` | Redis, S3 | Key = sha256(model_version\|voice\|normalize\|canonical_params\|normalized_text) |
| Dispatcher | `internal/dispatch` | Worker pool | Bounded priority queue; 30% slots reserved for stream class |
| Worker client pool | `internal/dispatch` | gRPC | Health polling, least-busy selection, circuit breaker per worker |
| Synth use case | `internal/synth` | cache, dispatch, usage | Orchestrates hit/miss, tees stream to client and cache writer |
| Job orchestrator | `internal/jobs` | Redis Streams, Postgres, dispatch, S3 | State machine, segment retry, merge, webhook |
| Usage meter | `internal/usage` | Postgres, Redis | Async batch insert; daily rollup cron |

### Level 3 — Worker components

| Component | Notes |
|-----------|-------|
| gRPC servicer | Implements `Synthesize`, `Segment`, `Health`; honours context cancellation |
| Engine | Loads ZeroTTS once; `intra_op_threads` from env; single-flight lock per process |
| Preprocess | `normalize_vi_text`, `chunk_text`; pure, unit-tested |
| Encoder | PCM16 passthrough for stream; Opus/MP3 via ffmpeg subprocess for final files; resample via scipy |
| Health | Reports `ready`, `slots_total`, `slots_busy`, `model_version`, `rtf_ewma` |

## 4. Trust boundaries

| Boundary | Crossing | Controls |
|----------|----------|----------|
| Internet → gateway | Tenant request | TLS, API key, rate limit, body size limit 1 MiB, strict JSON schema |
| Gateway → worker | gRPC | Private network; deadline propagated; per-call max text length enforced again on worker |
| Gateway → Postgres/Redis | Queries | All tenant-scoped queries include `tenant_id`; no raw SQL string concat (sqlc) |
| Gateway → S3 | Read/write | Least-privilege IAM; signed URLs expire 24 h; bucket private |
| Gateway → tenant webhook | Outbound POST | HMAC-SHA256 signature, retries with backoff, SSRF guard (no private IP ranges) |
| Worker → Hugging Face | Boot-time download | Pinned revision hash; mirrored to S3; verified checksum |
| Operator → admin API | Admin endpoints | Separate admin key scope, IP allowlist, audit log |

## 5. Cross-cutting concerns

| Concern | Decision |
|---------|----------|
| Configuration | 12-factor env vars, validated at boot, fail fast (see `08-variables.md`) |
| Errors | RFC 9457 problem+json; stable `code` strings; no stack traces to clients |
| Logging | Structured JSON (zerolog / structlog), request id, tenant id; never log text or keys |
| Tracing | OpenTelemetry; trace id propagated over gRPC metadata |
| Metrics | Prometheus; RED for HTTP, plus `tts_ttfa_seconds`, `tts_rtf`, `dispatch_queue_depth`, `cache_hit_total`, `usage_chars_total{tenant}` |
| Timeouts | Every outbound call has a deadline; stream idle timeout 30 s; sync max 60 s |
| Graceful shutdown | Gateway drains in-flight streams up to 30 s; worker finishes current utterance then exits |
| Idempotency | Jobs via `Idempotency-Key` (24 h); usage insert dedups on request id |
| Migrations | goose, forward-only, applied by CI job before deploy |
| Feature flags | Simple env flags for MVP; no external flag service |

## 6. Known risks and assumptions (evidence-backed)

| Risk | Where it shows | Mitigation |
|------|----------------|------------|
| Worker CPU saturation collapses TTFA | `dispatch/` reservation logic; NFR-01 | Admission control, stream slot reservation, alert on `dispatch_queue_wait_seconds` p95 > 1 s |
| Cache poisoning across tenants | `cache/` key derivation | Key excludes tenant on purpose (audio is deterministic per input); text is not stored; hit only returns audio, never metadata of other tenants |
| Client disconnect leaves worker computing | `synth/` context propagation | Cancellation tested end-to-end in `10-tests.md` T-07 |
| Redis loss resets quotas | `quota/` | Redis AOF + hourly reconciliation from Postgres usage |
| Weight download failure at boot | `worker/engine.py` | S3 mirror, readiness gate, retry with backoff |

## 7. Related documents

`01-PRD.md`, `adr/`, `api/openapi.yaml`, `api/worker.proto`, `05-data-model.md`,
`06-flows.md`, `07-permissions.md`, `08-variables.md`, `10-tests.md`, `13-runbook.md`.

No transactional email in v1 — no `emails.md`. Scheduled work exists (usage rollup,
cache eviction, quota reconciliation) and is documented in `13-runbook.md` §Scheduled
jobs. No embedded LLM agents — no `automation.md`; webhooks are covered in `06-flows.md`.
