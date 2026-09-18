# Test map

Rule → expected behaviour → evidence → status. CI-required rows gate merges to `main`.
Status legend: **existing** (in repo), **proposed** (to write in the stated milestone),
**gap** (no verification planned yet).

## Test pyramid

| Layer | Tooling | Scope | Runs |
|-------|---------|-------|------|
| Unit (Go) | `go test`, table-driven, `testify` | pure logic: cache key, limiter Lua wrapper (miniredis), state machine, SSRF guard | every PR |
| Unit (Python) | `pytest` | preprocess, encode, frame sequencing with a fake engine | every PR |
| Contract | `buf breaking`, `oapi-codegen` diff | proto and OpenAPI drift | every PR |
| Integration (Go) | `testcontainers-go` (Postgres, Redis, MinIO) + fake worker gRPC server | flows FL-01..05 | every PR |
| E2E | compose + real worker (small text) | smoke: sync, stream, job | nightly and release |
| Load | k6 | NFR-01/02/05 | release |
| Security | `govulncheck`, `pip-audit`, `gitleaks`, ZAP baseline | deps, secrets, headers | every PR / weekly |

## Verification map

| ID | Use case | Rule | Expected (incl. deny) | Evidence | Type | Status | CI |
|----|----------|------|-----------------------|----------|------|--------|----|
| T-01 | US-01 | Frames ordered, last flag | seq 0..n, last=true once; rate/duration ±1%; invalid input → INVALID_ARGUMENT; busy → RESOURCE_EXHAUSTED | `worker/tests/test_stream.py` | unit | done (0.4) | yes |
| T-02 | US-01 | Cancel stops inference | cancel → generator closed, slot free ≤ 200 ms | `worker/tests/test_cancel.py` | unit + `-m model` | done (0.4) | yes |
| T-03 | US-05 | Auth | valid/revoked/suspended/no-scope → 200/401/401/403 | `gateway/internal/auth/*_test.go`, `http/auth_integration_test.go` | unit+int | proposed M1 | yes |
| T-04 | US-08 | Validation | >3000 chars → 413; unknown voice → 422; bad JSON → 400 problem+json | `http/synth_test.go` | int | proposed M1 | yes |
| T-05 | US-06 | Limiter atomic | 100 concurrent → exactly `limit` succeed | `ratelimit/lua_test.go` (miniredis) | unit | proposed M1 | yes |
| T-06 | US-07 | Quota | at limit×1.05 → 402; reconcile fixes drift | `quota/*_test.go` | int | proposed M1 | yes |
| T-07 | US-09 | Disconnect cancels worker | close client conn → fake worker sees ctx cancel ≤ 200 ms; lease released | `synth/stream_cancel_test.go` | int | proposed M1 | yes |
| T-08 | US-11 | Cache hit bypasses worker | 2nd identical request → HIT, fake worker call count unchanged; billed | `cache/*_test.go`, `synth/cache_hit_test.go` | int | proposed M1 | yes |
| T-09 | US-14 | Job resume | kill orchestrator mid-job → restart → done segments not re-run | `jobs/resume_test.go` | int | proposed M2 | yes |
| T-10 | US-15 | SSRF guard | `http://`, `127.0.0.1`, `10.x`, `169.254.x`, DNS→private → 422 | `jobs/webhook_guard_test.go` | unit | proposed M2 | yes |
| T-11 | NFR-07 | No secrets/text in logs | run suite with log capture; grep for key secret and sample text → none | `telemetry/redaction_test.go` | int | proposed M1 | yes |
| T-12 | 07-permissions | Tenant isolation | tenant A cannot read B's job/usage/keys → 404/empty | `http/isolation_test.go` | int | proposed M2 | yes |
| T-13 | US-12 | Overload rejects fast | k6 3× → p95 503 latency < 50 ms; stream still served | `scripts/loadtest/spike.js` | load | proposed M3 | release |
| T-14 | US-09 | TTFA SLO | k6 80% util → p95 ≤ 300 ms | `scripts/loadtest/steady.js` | load | proposed M3 | release |
| T-15 | US-14 | Idempotency | same key+body → same job; different body → 409 | `jobs/idempotency_test.go` | int | proposed M2 | yes |
| T-16 | US-04 | Merge duration | sum + gaps ± 50 ms | `worker/tests/test_merge.py` | unit | proposed M2 | yes |
| T-17 | ADR-009 | Contract drift | regenerate → no diff | `make generate && git diff --exit-code` | contract | done (0.2) | yes |
| T-18 | US-17 | Key revoke immediate | revoke → next call 401 | `http/keys_test.go` | int | proposed M2 | yes |
| T-19 | FL-07 | Scheduled jobs single-run | two replicas → lock → one execution | `sched/lock_test.go` | int | proposed M3 | yes |
| T-20 | NFR-12 | Restore drill | restore dump into fresh DB → migrations idempotent | `13-runbook.md` procedure | manual | gap | — |
| T-21 | US-02 | Health before weights | `ready=false`, `model_version` = pinned revision, answers over gRPC while loading | `worker/tests/test_server.py` | unit | done (0.3) | yes |
| T-22 | US-02 | Health state is real | `slots_busy` follows the single slot; `rtf_ewma` folds every request | `worker/tests/test_health.py` | unit | done (0.3) | yes |
| T-23 | US-02 | Engine loads pinned weights | load → `ready=true`, shipped voices listed, RTF < 1.0 on CPU | `worker/tests/test_engine_model.py` (`-m model`) | unit (opt-in) | done (0.3) | no |
| T-27 | NFR-06 | Problem mapping | every `Problem.code` maps to its status; an unmapped error becomes `internal` with no detail | `internal/http/problem_test.go` | unit | done (1.1) | yes |
| T-28 | FL-06 | Readiness registry | no checks → ready; a failing check → 503 naming it, probe error not leaked | `internal/http/router_test.go` | unit | done (1.1) | yes |
| T-29 | NFR-07 | Request id is safe | caller id echoed, sanitised and length-capped; credentials never reach the access log | `internal/http/router_test.go` | unit | done (1.1) | yes |
| T-26 | US-02 | Stack smoke | `make up` → worker healthy; one streamed synthesis, frames ordered, `last=true` present | `scripts/smoke.sh` | e2e | done (0.6, gateway leg 1.1) | no |
| T-25 | US-01 | First frame latency | TTFA ≤ 150 ms for a 21-char input on the bench machine | `worker/tests/test_engine_model.py` (`-m model`) | unit (opt-in) | done (0.4) | no |
| T-24 | NFR-06 | Worker config validation | missing revision / partial `VITTS_S3_*` → exit non-zero with a named variable | `worker/tests/test_config.py` | unit | done (0.3) | yes |

## Gaps (ranked)

1. T-20 backup restore is manual; automate quarterly.
2. Audio quality regression (WER via PhoWhisper) has no harness; proposed for M4 using upstream `eval` extra on a fixed 50-sentence corpus.
3. T-23 is excluded from CI (it downloads ~200 MB of weights); run it in the nightly
   job that lands with 0.5 bench.
4. WebSocket cancel semantics (US-10) share T-07 logic but need their own integration test in M1.
