# Implementation plan

Order matters: each task is a single PR with tests, sized for one Claude Code session.
Definition of done for every task: code + tests + docs updated + CI green + `make smoke`
passes locally where applicable. Do not start a task whose dependencies are unmerged.

## M0 — Spike and foundations (week 1–2) — **done**, see `docs/reports/m0-summary.md`

| # | Task | Depends | Output | Status |
|---|------|---------|--------|--------|
| 0.1 | Repo scaffold: `Makefile`, `.editorconfig`, `.gitignore`, `CODEOWNERS`, CI skeleton (lint only) | — | green CI on empty repo | done `a1f06f4` |
| 0.2 | `proto/worker.proto` + `buf` config + generated Go/Python stubs checked in; T-17 | 0.1 | `make generate` | done `c7ab53f` |
| 0.3 | Worker: engine wrapper loading ZeroTTS, env config, `Health` RPC, warm-up, S3 mirror | 0.2 | US-02 | done `4847aaf` |
| 0.4 | Worker: `Synthesize` streaming with cooperative cancel; T-01, T-02 | 0.3 | US-01 | done `dc95e82` |
| 0.5 | `scripts/bench.py`: RTF/TTFA for 1×8, 2×4 threads on target box; write `docs/reports/bench-m0.md` and update A1/A2 | 0.4 | numbers | done `c11544f` |
| 0.6 | Compose: worker + MinIO + Postgres + Redis; `make smoke` calls Health | 0.3 | local stack | done `c11544f` |

## M1 — Core API (week 3–5) — **done**

| # | Task | Depends | Output | Status |
|---|------|---------|--------|--------|
| 1.1 | Gateway skeleton: config, chi router, problem+json, request id, zerolog, OTel, `/healthz`, `/readyz`, graceful shutdown | 0.6 | boots | done `1e49ee6` |
| 1.2 | Migrations 0001 (plans, tenants, api_keys, voices, audio_cache, synth_requests partitioned, audit_log) + sqlc | 1.1 | schema | done `83d3cf8` |
| 1.3 | Auth middleware + admin key + `/admin/v1/tenants`; T-03 | 1.2 | US-05 | done `4d062fd` |
| 1.4 | Rate limiter + concurrency lease (Lua); T-05 | 1.3 | US-06 | done `a249ad7` |
| 1.5 | Quota service + Redis counter + reconcile job; T-06 | 1.4 | US-07 | done `a249ad7` |
| 1.6 | Worker client pool: health polling, least-busy, circuit breaker | 1.1 | FL-06 | done `fcd15be` |
| 1.7 | Dispatcher: bounded priority queue, slot reservation, `Retry-After` | 1.6 | US-12 (logic) | done `a249ad7` |
| 1.8 | Cache manager: key derivation, Redis index, S3 store; T-08 | 1.2 | US-11 | done `a249ad7` |
| 1.9 | Synth use case + `POST /v1/synthesize` (wav/mp3/ogg via worker `Merge` for non-wav); T-04 | 1.5,1.7,1.8 | US-08 | done `008fa73` |
| 1.10 | Streaming endpoint (HTTP chunked) with tee-to-cache and cancel; T-07 | 1.9 | US-09 | done `93914a9` |
| 1.11 | WebSocket endpoint with cancel message | 1.10 | US-10 | done this PR |
| 1.12 | Usage meter (async batch insert) + rollup job | 1.9 | US-16 backend | done `93914a9` |
| 1.13 | Voices endpoint + preview generation script | 1.2 | US-13 | done `008fa73` |
| 1.14 | Log redaction test T-11; OpenAPI served at `/openapi.json`; generated handlers via oapi-codegen | 1.9 | contract | done this PR |

## M2 — Jobs and tenant self-service (week 6–7)

| # | Task | Depends | Output |
|---|------|---------|--------|
| 2.1 | Migrations 0002 (jobs, job_segments, usage_daily) | 1.12 | schema |
| 2.2 | Worker `Segment` and `Merge`; T-16 | 0.4 | US-03, US-04 |
| 2.3 | Job API: create with idempotency, get, list, cancel; T-15, T-12 | 2.1 | US-14 (API) |
| 2.4 | Orchestrator: Redis Streams consumer, state machine, segment fan-out, retry, merge, reconciler; T-09 | 2.2,2.3 | US-14 |
| 2.5 | Webhook dispatcher with HMAC and SSRF guard; T-10 | 2.4 | US-15 |
| 2.6 | Keys API; T-18 | 1.3 | US-17 |
| 2.7 | Usage API JSON/CSV | 1.12 | US-16 |
| 2.8 | Cache eviction + partition maintenance jobs with Redis lock; T-19 | 1.8 | FL-07 |

## M3 — Launch readiness (week 8–9)

| # | Task | Depends | Output |
|---|------|---------|--------|
| 3.1 | Prometheus metrics complete, Grafana dashboards, alert rules | M1 | US-18 |
| 3.2 | k6 scenarios steady/spike/cache; run and publish `docs/reports/load-m3.md`; T-13, T-14 | M2 | US-20 |
| 3.3 | Prod compose, secrets from env store, non-root images, digest pins, `govulncheck`/`pip-audit`/`gitleaks` in CI | M2 | US-19 |
| 3.4 | Runbook drills: worker replace, Redis restart, restore from backup (T-20) | 3.3 | 13-runbook |
| 3.5 | Minimal console (Next.js or HTMX): login via key, usage chart, key management | 2.6,2.7 | F-12 |
| 3.6 | Python + JS SDK thin clients generated from OpenAPI; docs site | 1.14 | F-14 |

## M4 — Post-launch

Pricing iteration, output resampling on cache hits, WER regression harness, tenant
voices (F-15) when vendor supplies latents, second worker node and gateway replica.
