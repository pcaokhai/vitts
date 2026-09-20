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
| T-03 | US-05 | Auth | valid/revoked/suspended/no-scope → 200/401/401/403; lookup outage → 500, not 401 | `gateway/internal/auth/*_test.go`, `http/auth_test.go`, `http/auth_integration_test.go` | unit+int | done (1.3) | yes |
| T-35 | US-05 | Tenant creation is atomic | tenant, first key and audit row all commit or none do; secret returned once | `http/auth_integration_test.go` | int | done (1.3) | yes |
| T-36 | NFR-07 | Admin needs key and network | wrong key, wrong network, or spoofed `X-Forwarded-For` → 401; empty allowlist denies all | `http/auth_test.go` | unit | done (1.3) | yes |
| T-37 | NFR-07 | Admin config fails closed | short `VITTS_ADMIN_KEY` or empty allowlist → process does not boot | `internal/config/config_test.go` | unit | done (1.3) | yes |
| T-04 | US-08 | Validation | >3000 chars → 413; unknown voice → 422; bad JSON → 400 problem+json | `internal/synth/synth_test.go`, verified against the live stack | unit | done (1.9) | yes |
| T-05 | US-06 | Limiter atomic | 100 concurrent → exactly `limit` succeed; leases likewise | `ratelimit/lua_test.go` (miniredis) | unit | proposed M1 | yes |
| T-06 | US-07 | Quota | at limit×1.05 → 402; reconcile fixes drift both ways and seeds a flushed Redis | `quota/*_test.go` | int | proposed M1 | yes |
| T-07 | US-09 | Disconnect cancels worker | close client conn → worker call cancelled, lease released, partial audio not cached | `internal/synth/stream_test.go`, live stack | unit | done (1.10) | yes |
| T-08 | US-11 | Cache hit bypasses worker | 2nd identical request → HIT, byte-identical, and the fake worker call count unchanged; billed | `cache/*_test.go`, `synth/cache_hit_test.go` | int | proposed M1 | yes |
| T-09 | US-14 | Job resume | replayed job and segment entries after a crash → finished segments are not re-synthesized | `internal/jobs/orchestrator_test.go` | unit | done (2.4) | yes |
| T-10 | US-15 | SSRF guard | http, loopback, RFC1918, link-local, CGNAT, IPv6 ULA, multicast, mixed DNS answer → 422 | `internal/jobs/webhook_guard_test.go`, live stack | unit | done (2.3) | yes
| T-11 | NFR-07 | No secrets/text in logs | log capture contains metadata and a digest prefix, never a key secret, an Authorization header or request text | `internal/telemetry/redaction_test.go` | unit | done (1.14) | yes |
| T-12 | 07-permissions | Tenant isolation | tenant A cannot read, cancel or list B's job → 404/empty | `internal/jobs/service_test.go` | unit | done (2.3) | yes |
| T-13 | US-12 | Overload rejects fast | k6 at 24× capacity → p95 503 latency 16 ms (budget 50); streams still served | `scripts/loadtest/spike.js`, `docs/reports/load-m3.md` | load | done (3.2) | no |
| T-14 | US-09 | TTFA SLO | k6 at capacity → TTFA p95 133 ms (budget 300), p99 143 ms (600) | `scripts/loadtest/steady.js`, `docs/reports/load-m3.md` | load | done (3.2) | no |
| T-15 | US-14 | Idempotency | same key+body → same job; different body → 409; whitespace-only differences still match | `internal/jobs/service_test.go`, live stack | unit | done (2.3) | yes |
| T-16 | US-04 | Merge duration | sum + gaps ± 50 ms; measured +0 ms on 6 real segments | `worker/tests/test_segment_merge.py`, live stack | unit | done (2.2) | yes |
| T-17 | ADR-009 | Contract drift | regenerate → no diff | `make generate && git diff --exit-code` | contract | done (0.2) | yes |
| T-18 | US-17 | Key revoke immediate | revoke → next call 401 with no cache wait; scope denied → 403 | `internal/auth/keys_test.go`, live stack | unit | done (2.6) | yes |
| T-19 | FL-07 | Scheduled jobs single-run | two replicas on one schedule → the lock lets exactly one run; a failure still releases | `internal/maintenance/scheduler_test.go` | unit | done (2.8) | yes |
| T-20 | NFR-12 | Restore drill | restore dump into fresh DB → migrations idempotent | `13-runbook.md` procedure | manual | gap | — |
| T-21 | US-02 | Health before weights | `ready=false`, `model_version` = pinned revision, answers over gRPC while loading | `worker/tests/test_server.py` | unit | done (0.3) | yes |
| T-22 | US-02 | Health state is real | `slots_busy` follows the single slot; `rtf_ewma` folds every request | `worker/tests/test_health.py` | unit | done (0.3) | yes |
| T-23 | US-02 | Engine loads pinned weights | load → `ready=true`, shipped voices listed, RTF < 1.0 on CPU | `worker/tests/test_engine_model.py` (`-m model`) | unit (opt-in) | done (0.3) | no |
| T-27 | NFR-06 | Problem mapping | every `Problem.code` maps to its status; an unmapped error becomes `internal` with no detail | `internal/http/problem_test.go` | unit | done (1.1) | yes |
| T-28 | FL-06 | Readiness registry | no checks → ready; a failing check → 503 naming it, probe error not leaked | `internal/http/router_test.go` | unit | done (1.1) | yes |
| T-29 | NFR-07 | Request id is safe | caller id echoed, sanitised and length-capped; credentials never reach the access log | `internal/http/router_test.go` | unit | done (1.1) | yes |
| T-30 | ADR-006 | Migration ships no pricing | fresh database → `plans` is empty | `internal/storage/postgres/postgres_integration_test.go` | int | done (1.2) | yes |
| T-31 | FL-06 | Postgres readiness | pool up → ready; pool closed → `/readyz` 503 naming postgres | `postgres_integration_test.go`, `internal/http/router_test.go` | int + unit | done (1.2) | yes |
| T-32 | US-17 | Revoked key stops resolving | revoke → lookup by hash finds nothing; another tenant cannot revoke | `postgres_integration_test.go` | int | done (1.2) | yes |
| T-33 | NFR-11 | Partition routing | row lands in its month's partition; a month with no partition errors | `postgres_integration_test.go` | int | done (1.2) | yes |
| T-34 | FL-07 | Partition helper is idempotent | calling twice for one month yields the same partition | `postgres_integration_test.go` | int | done (1.2) | yes |
| T-38 | FL-06 | Worker readiness gates /readyz | no ready worker → 503 naming workers; one ready → 200 | `internal/dispatch/pool_test.go` | unit | done (1.6) | yes |
| T-39 | FL-06 | Circuit opens on 3 failures | 3 consecutive probe failures → ejected and unselectable | `internal/dispatch/pool_test.go` | unit | done (1.6) | yes |
| T-40 | FL-06 | Circuit closes on recovery | a successful re-probe clears ejection; fleet survives one sick worker | `internal/dispatch/pool_test.go` | unit | done (1.6) | yes |
| T-41 | FL-06 | Least-busy selection | more free slots wins; equal slots break on RTF; loading or busy is unselectable | `internal/dispatch/pool_test.go` | unit | done (1.6) | yes |
| T-42 | US-06 | Lease cap and release | third stream at limit 2 refused; release frees it; tenants isolated | `internal/ratelimit/ratelimit_integration_test.go` | int | done (1.4) | yes |
| T-43 | US-06 | Lease expiry needs no sweeper | a lease written in the past is evicted by the next acquire; renewing a reclaimed lease errors | `internal/ratelimit/ratelimit_integration_test.go` | int | done (1.4) | yes |
| T-44 | US-06 | Limit headers and fail-closed | 429 carries `Retry-After` ≥ 1 and `X-RateLimit-*`; limiter outage → 500, never allow | `internal/http/ratelimit_test.go` | unit | done (1.4) | yes |
| T-45 | US-07 | Quota grace and accounting | refuses past limit×1.05; `Check` consumes nothing; zero allowance is metered | `internal/quota/quota_integration_test.go` | int | done (1.5) | yes |
| T-46 | US-12 | Overload refuses, never queues | saturated fleet → `ErrOverloaded` inside the class budget, not a timeout | `internal/dispatch/queue_test.go` | unit | done (1.7) | yes |
| T-47 | ADR-007 | Stream reservation | batch stops at 70% of slots; a stream may still take the reserved ones | `internal/dispatch/queue_test.go` | unit | done (1.7) | yes |
| T-48 | US-12 | Retry-After is bounded | refusal carries 1 s ≤ Retry-After ≤ 30 s | `internal/dispatch/queue_test.go` | unit | done (1.7) | yes |
| T-49 | US-12 | Slot accounting is not stale | a held reservation is not free before the next health poll; release wakes a waiter | `internal/dispatch/queue_test.go` | unit | done (1.7) | yes |
| T-50 | US-11 | Cache key canonicalisation | 0.80=0.8, whitespace variants hit; model/voice/case/punctuation/rate miss | `internal/cache/key_test.go` | unit | done (1.8) | yes |
| T-51 | US-11 | Cache manager ordering | store writes the object before indexing; a vanished object degrades to a miss and drops the stale entry | `internal/cache/cache_test.go` | unit | done (1.8) | yes |
| T-52 | US-05 | /v1 guard chain | registered route → 401 without a key, 403 without the scope, 429 with Retry-After when throttled | `internal/http/ratelimit_test.go` | unit | done (1.4) | yes |
| T-53 | US-08 | WAV container | header is RIFF/WAVE PCM16 mono at the requested rate; duration matches byte count | `internal/audio`, `internal/synth/service_test.go` | unit | done (1.9) | yes |
| T-54 | US-08 | Contract bounds | 3000 characters (not bytes) fit; 3001 → 413; unknown format/rate → 400; mp3 refused naming task 2.2 | `internal/synth/synth_test.go` | unit | done (1.9) | yes |
| T-55 | US-08 | Refusals cost nothing | quota exceeded and unknown voice never reach a worker and never bill | `internal/synth/service_test.go` | unit | done (1.9) | yes |
| T-56 | US-13 | Catalogue follows the fleet | voices are upserted from ready workers; listing is sorted and tenant-scoped | `internal/voices`, live stack | unit | done (1.13) | yes |
| T-57 | US-09 | Stream frames and tee | header once before any audio; frames delivered in order; completed stream is cached | `internal/synth/stream_test.go` | unit | done (1.10) | yes |
| T-58 | US-06 | Stream lease lifecycle | at the plan limit → concurrency_limited; a cancelled stream frees its slot at once | `internal/synth/stream_test.go` | unit | done (1.10) | yes |
| T-59 | US-16 | Meter never blocks | a stalled writer drops records and counts them instead of blocking Record | `internal/usage/meter_test.go` | unit | done (1.12) | yes |
| T-60 | US-16 | Meter drains on shutdown | queued records are written before Stop returns | `internal/usage/meter_test.go` | unit | done (1.12) | yes |
| T-61 | ADR-010 | Cancelled stream billing | billed characters come from delivered audio, capped at the request | `internal/synth/stream_test.go`, live stack | unit | done (1.10) | yes |
| T-62 | US-10 | WebSocket utterance | request frame → binary audio then `{"type":"end"}` with duration | `internal/http/websocket_test.go` | unit | done (1.11) | yes |
| T-63 | US-10 | Barge-in keeps the socket | cancel stops the utterance and the next request succeeds on the same socket; every slot released | `internal/http/websocket_test.go`, live stack | unit | done (1.11) | yes |
| T-64 | US-01 | Slot released after cancel | worker frees its inference slot ≤ 200 ms after a client cancel | measured against the live worker | manual | done (1.11) | no |
| T-65 | ADR-009 | Contract is served | `/openapi.json` and `/openapi.yaml` serve the embedded contract without a credential | `internal/http/openapi_test.go` | unit | done (1.14) | yes |
| T-66 | ADR-009 | No undeclared error codes | every `Problem.code` the gateway emits is in the contract's enum | `internal/http/openapi_test.go` | unit | done (1.14) | yes |
| T-67 | US-03 | Segmentation is lossless | concatenated segments keep every non-whitespace character | `worker/tests/test_segment_merge.py` | unit | done (2.2) | yes |
| T-68 | US-03 | Segmentation is fast | 100,000 characters segment in under 2 s | `worker/tests/test_segment_merge.py` | unit | done (2.2) | yes |
| T-69 | US-03 | Segment needs no weights | `Segment` answers before the model has loaded | `worker/tests/test_server.py` | unit | done (2.2) | yes |
| T-70 | US-04 | Containers decode | wav, mp3 and ogg_opus all decode back at the requested rate; 59/31 kbps measured | `worker/tests/test_segment_merge.py`, live stack | unit | done (2.2) | yes |
| T-71 | US-04 | Missing segment is named | absent object → FAILED_PRECONDITION carrying the key | live stack, `worker/tests/test_server.py` | unit | done (2.2) | yes |
| T-72 | US-14 | Job creation is durable | text stored before the row, queue last; a queue failure still returns a queued job | `internal/jobs/service_test.go` | unit | done (2.3) | yes |
| T-73 | US-14 | Cancel is idempotent | cancelling twice succeeds and stays cancelled | `internal/jobs/service_test.go` | unit | done (2.3) | yes |
| T-74 | US-14 | Plan job limit | text past the plan's max_job_chars → 413 | `internal/jobs/service_test.go` | unit | done (2.3) | yes |
| T-75 | ADR-008 | Internal metadata is private | the idempotency fingerprint never appears in a job response | `internal/jobs/service_test.go` | unit | done (2.3) | yes |
| T-76 | US-14 | Job reaches completed | queued → segmenting → synthesizing → merging → completed with a usable output link | `internal/jobs/orchestrator_test.go`, live stack | unit | done (2.4) | yes |
| T-77 | US-14 | Segment retry then fail | a segment retries to 3 attempts, then the job fails with the reason | `internal/jobs/orchestrator_test.go` | unit | done (2.4) | yes |
| T-78 | US-14 | Transient failure recovers | a segment that fails once still completes the job | `internal/jobs/orchestrator_test.go` | unit | done (2.4) | yes |
| T-79 | US-14 | Cancel stops dispatch | a cancelled job dispatches no further segments and never reaches the model | `internal/jobs/orchestrator_test.go` | unit | done (2.4) | yes |
| T-80 | US-14 | Failures are recorded | unreadable input or a failed merge fails the job with a reason | `internal/jobs/orchestrator_test.go` | unit | done (2.4) | yes |
| T-81 | US-15 | Webhook is signed | `sha256=` HMAC over timestamp and body, verifiable by the receiver | `internal/webhook/webhook_test.go` | unit | done (2.5) | yes |
| T-82 | US-15 | Signature resists replay | the same body with a different timestamp does not verify | `internal/webhook/webhook_test.go` | unit | done (2.5) | yes |
| T-83 | US-15 | Webhook retries and stops | retries until 2xx, gives up after 5 attempts | `internal/webhook/webhook_test.go` | unit | done (2.5) | yes |
| T-84 | US-15 | Guard runs per attempt | a URL that turns private between attempts is not called | `internal/webhook/webhook_test.go` | unit | done (2.5) | yes |
| T-85 | US-17 | No scope escalation | a key cannot mint a key with scopes it does not hold | `internal/auth/keys_test.go` | unit | done (2.6) | yes |
| T-86 | ADR-008 | Webhook hides bookkeeping | the payload carries tenant metadata but not internal keys | `internal/webhook/webhook_test.go` | unit | done (2.5) | yes |
| T-87 | NFR-07 | Webhook secret required | a secret under 32 characters fails the boot | `internal/config/config_test.go` | unit | done (2.5) | yes |
| T-88 | US-16 | Usage totals | per-day rows sum into period totals shown against the plan limit | `internal/usage/report_test.go`, live stack | unit | done (2.7) | yes |
| T-89 | US-16 | Report window bounds | backwards or over-long windows → 400; days normalise to UTC | `internal/usage/report_test.go` | unit | done (2.7) | yes |
| T-90 | US-16 | CSV is RFC 4180 | header row, CRLF endings, parses with a standard reader, valid when empty | `internal/usage/report_test.go`, live stack | unit | done (2.7) | yes |
| T-91 | NFR-11 | Eviction ordering | object deleted before its row; a failed object delete keeps the row for the next sweep | `internal/maintenance/scheduler_test.go` | unit | done (2.8) | yes |
| T-92 | FL-07 | Partition look-ahead | maintenance creates the next two months' partitions | `internal/maintenance/scheduler_test.go` | unit | done (2.8) | yes |
| T-93 | FL-07 | Rollup is idempotent | the rollup window overlaps its interval so a missed tick self-heals | `internal/maintenance/scheduler_test.go`, live stack | unit | done (2.8) | yes |
| T-94 | US-18 | Contract metrics exist | every metric US-18 names is registered and gathers | `internal/telemetry/metrics_test.go` | unit | done (3.1) | yes |
| T-95 | US-18 | Label cardinality is bounded | status buckets to its class; unmatched routes share one label; tenant label is length-checked | `internal/telemetry/metrics_test.go`, `internal/http/metrics_test.go` | unit | done (3.1) | yes |
| T-96 | US-18 | Metrics endpoint | `/metrics` serves without a credential; a router without metrics still serves | `internal/http/metrics_test.go` | unit | done (3.1) | yes |
| T-97 | US-18 | Alerts fire on real data | stopping the worker drives `worker_ready` to 0 and fires NoReadyWorker | live stack | manual | done (3.1) | no |
| T-98 | US-18 | Dashboard is provisioned | Grafana loads the committed dashboard JSON at startup | live stack | manual | done (3.1) | no |
| T-99 | ADR-007 | Queue is bounded | an overloaded dispatcher refuses in microseconds rather than after the class budget | `internal/dispatch/queue_test.go` | unit | done (3.2) | yes |
| T-100 | ADR-007 | Free capacity is never refused | while a slot is free every caller is admitted | `internal/dispatch/queue_test.go` | unit | done (3.2) | yes |
| T-101 | US-19 | Images are pinned and non-root | every FROM and compose image carries a digest; gateway runs as nonroot with no shell; worker as uid 10001 | live inspection | manual | done (3.3) | no |
| T-102 | US-19 | Production config fails closed | the overlay refuses to render when a secret is unset | `make prod-config` | manual | done (3.3) | no |
| T-103 | US-19 | Dependency scanning gates merges | govulncheck and pip-audit run in CI; unreviewed findings fail | `.github/workflows/ci.yml`, `scripts/audit.sh` | ci | done (3.3) | yes |
| T-104 | NFR-12 | Exceptions expire | an accepted finding past its review_by date fails the audit | `scripts/audit.sh`, `security/accepted.yaml` | ci | done (3.3) | yes |
| T-26 | US-02 | Stack smoke | `make up` → worker healthy; one streamed synthesis, frames ordered, `last=true` present | `scripts/smoke.sh` | e2e | done (0.6, gateway leg 1.1) | no |
| T-25 | US-01 | First frame latency | TTFA ≤ 150 ms for a 21-char input on the bench machine | `worker/tests/test_engine_model.py` (`-m model`) | unit (opt-in) | done (0.4) | no |
| T-24 | NFR-06 | Worker config validation | missing revision / partial `VITTS_S3_*` → exit non-zero with a named variable | `worker/tests/test_config.py` | unit | done (0.3) | yes |

## Gaps (ranked)

1. T-20 backup restore is manual; automate quarterly.
2. Audio quality regression (WER via PhoWhisper) has no harness; proposed for M4 using upstream `eval` extra on a fixed 50-sentence corpus.
3. T-23 is excluded from CI (it downloads ~200 MB of weights); run it in the nightly
   job that lands with 0.5 bench.
4. WebSocket cancel semantics (US-10) share T-07 logic but need their own integration test in M1.
