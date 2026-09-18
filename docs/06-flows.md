# Flows

Only flows that touch permissions, money, data integrity, external side effects or
operational safety. Feature behaviour is in the PRD and stories.

## FL-01 Streaming synthesis (cache miss)

Actor: tenant app with key scope `synth`. Precondition: active tenant, quota not
exhausted. Success: audio streamed, usage recorded.

| Step | Layer | Action | Authz / guard | Side effects |
|------|-------|--------|---------------|--------------|
| 1 | HTTP | Parse JSON (≤ 1 MiB), validate schema | — | — |
| 2 | Auth | Hash bearer, lookup Redis→Postgres, check `revoked_at`, `tenant.status` | key valid, scope `synth` | `last_used_at` (async) |
| 3 | Rate limit | Token bucket per key | 429 `rate_limited` | — |
| 4 | Concurrency | Acquire lease in `conc:{tenant}` | 429 `concurrency_limited` | Lease with 60 s TTL, renewed per chunk |
| 5 | Quota | `usage:{tenant}:{month}` + chars ≤ limit × 1.05 | 402 `quota_exceeded` | — |
| 6 | Cache | Derive key; Redis lookup | — | — |
| 7 | Dispatch | Reserve slot, class `stream`, wait ≤ 2 s | 503 `overloaded` | Queue metrics |
| 8 | gRPC | `Synthesize` with deadline = 60 s, ctx tied to client conn | — | Worker CPU |
| 9 | Stream | Tee each frame to client and cache buffer; write RIFF header on first frame | — | Bytes to client |
| 10 | Finish | Upload PCM to S3, set `cache:` index, insert `audio_cache` | — | S3 write, Redis, Postgres |
| 11 | Meter | Enqueue usage record (chars, duration, ttfa, cached=false) | — | Postgres batch, Redis incr |
| 12 | Release | Release lease and slot | — | — |

Deny/abort cases: client disconnect at step 9 → cancel gRPC, discard cache buffer,
meter `chars_consumed` from last frame with status `client_cancelled`, release lease.
Worker error → 502 `internal` if no bytes sent yet, otherwise truncate stream and log.

Trust crossings: browser/app→gateway (1–2), gateway→worker (8), gateway→S3 (10).

## FL-02 Synthesis cache hit

Steps 1–6 as FL-01. Step 7: stream object from S3 (range read) or return signed URL
for `format=wav` when `Accept` allows redirect. Encode to mp3/ogg on the fly via worker
`Merge` only if requested format is not PCM/WAV (rare; usually cache stores PCM and
gateway wraps as WAV). Meter with `cached=true`. Bump `hit_count`, `last_hit_at`
(async, batched).

## FL-03 Job lifecycle

Actor: key scope `jobs`. Precondition: text ≤ plan `max_job_chars`; quota.

| Step | Action | Guard | Side effects |
|------|--------|-------|--------------|
| 1 | Validate; check `idem:{tenant}:{key}` → return existing job (202) | `409 idempotency_conflict` if same key with different body hash | — |
| 2 | Upload input text to S3 | — | S3 |
| 3 | Insert `jobs` row status `queued` (unique on idempotency) | — | Postgres |
| 4 | `XADD jobs:pending` | — | Redis |
| 5 | Orchestrator claims (XREADGROUP), sets `segmenting` (conditional update) | — | — |
| 6 | Worker `Segment` → insert `job_segments` pending, `segments_total` | — | Postgres |
| 7 | Set `synthesizing`; XADD one entry per segment to `jobs:segments` | — | Redis |
| 8 | Segment consumer: cache check → dispatch class `batch` → write `seg/{seq}.pcm` → mark `done`, `segments_done++` | retry ≤ 3 with backoff 5/30/120 s; then job `failed` | S3, Postgres |
| 9 | When `segments_done == segments_total` → `merging`; worker `Merge` → `output` | — | S3 |
| 10 | Set `completed`, `output_s3_key`, `duration_ms`; meter total chars once | — | Postgres |
| 11 | Webhook POST with HMAC; retry 5×; log attempts | SSRF guard: HTTPS, no RFC1918/loopback/link-local, DNS re-resolve | Outbound HTTP |

Cancel: `DELETE /v1/jobs/{id}` sets `cancelled` if non-terminal; segment consumers check
job status before dispatching; in-flight segment finishes but result is discarded.
Reconciler (every 60 s): `queued` jobs older than 2 min missing from stream → re-XADD;
PEL entries idle > 5 min → XCLAIM.

## FL-04 API key lifecycle

Create: needs scope `keys`; generate 32 random bytes → `zt_live_` + base62; store
sha256; return secret once; audit log. Revoke: set `revoked_at`, `DEL auth:{hash}`,
audit log; running streams complete. List: returns prefix only.

## FL-05 Admin tenant provisioning

Admin key (`X-Admin-Key`, env-provided, IP allowlist) creates tenant + first key. Every
admin action writes `audit_log`. Suspending a tenant flips `status`, invalidates all
`auth:` entries for its keys.

## FL-06 Worker health and routing

Gateway polls `Health` every 5 s per worker; a worker with 3 consecutive failures is
ejected (circuit open) and re-probed every 30 s. Model version mismatch between workers
is allowed during rolling deploy; cache key includes version so mixed fleets are safe.
`/readyz` on gateway is 200 only when ≥ 1 worker is ready.

## FL-07 Scheduled work

| Job | Schedule | Idempotency | Auth |
|-----|----------|-------------|------|
| Usage rollup | every 5 min | Upsert into `usage_daily` from `synth_requests` since watermark | internal |
| Quota reconcile | hourly | Recompute `usage:{tenant}:{month}` from `usage_daily` | internal |
| Cache eviction | daily 03:00 ICT | Delete `audio_cache` rows + S3 objects with `last_hit_at` < now − 30 d | internal |
| Job reconciler | every 60 s | See FL-03 | internal |
| Partition maintenance | monthly | Create next month's `synth_requests` partition | internal |

All run inside the gateway binary with a Redis lock (`SET NX PX`) so replicas do not
double-run.
