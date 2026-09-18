# User stories

Format: 3 C's (card, conversation, confirmation) and INVEST. Each story fits one
sprint task for Claude Code, is independently mergeable, and references requirements
(`F-xx`, `NFR-xx`) and tests (`T-xx` in `10-tests.md`). Personas: **Dev** (integrating
developer), **Ops** (operator/Khai), **Bot** (tenant's voice-bot backend).

## Epic E1 — Worker service

### US-01 Worker synthesizes and streams PCM frames
As Bot, I want the worker to stream PCM16 frames over gRPC as they are generated, so that
the gateway can forward audio with minimal delay. (F-02, NFR-01, ADR-003/004)

Acceptance criteria:
1. `Synthesize` returns frames with strictly increasing `seq` starting at 0 and a final frame with `last=true`.
2. First frame is emitted ≤ 150 ms after the call on the benchmark machine for a 26-char input.
3. If the client cancels the gRPC context, the worker stops inference within 200 ms and frees the slot.
4. Only one inference runs per process; a second concurrent call waits or is rejected with `RESOURCE_EXHAUSTED` when `slots_busy == slots_total`.
5. Text longer than `VITTS_MAX_TEXT_CHARS` is rejected with `INVALID_ARGUMENT`.
6. `output_sample_rate` 8000/16000/24000 produces resampled output with correct duration (±1%).

### US-02 Worker exposes health and model version
As Ops, I want a `Health` RPC reporting readiness, slots and model version, so that the
gateway can route safely and I can monitor the fleet. (FL-06)

Acceptance criteria:
1. `ready=false` until weights are loaded and a warm-up utterance has succeeded.
2. `model_version` equals the pinned HF revision.
3. `rtf_ewma` updates after every request.
4. Weights load from S3 mirror when present, else from HF Hub and then populate the mirror.

### US-03 Worker segments long text
As Dev, I want long text split into utterance-sized chunks by the upstream chunker, so
that job audio sounds natural. (F-03, F-05)

Acceptance criteria:
1. `Segment` applies `normalize_vi_text` when `normalize=true`, then `chunk_text` with `max_chunk_sec`.
2. Concatenated segments preserve every non-whitespace character of the input.
3. 100k-char input segments in < 2 s.

### US-04 Worker merges segments into a container format
As Dev, I want job segments merged into MP3/OGG/WAV with a short gap, so that I get one
downloadable file. (F-03)

Acceptance criteria:
1. `Merge` produces a file whose duration equals the sum of segment durations plus gaps (±50 ms).
2. Output uses the requested sample rate and format; MP3 at 64 kbps, Opus at 32 kbps.
3. Missing segment object → `FAILED_PRECONDITION` with the missing key in the message.

## Epic E2 — Gateway core

### US-05 Authenticate requests with API keys
As Dev, I want to authenticate with a bearer API key, so that only my apps use my quota.
(F-06, NFR-07)

Acceptance criteria:
1. Valid key → request proceeds; tenant id and key id attached to request context and logs.
2. Missing/malformed/revoked key → 401 `unauthorized`; suspended tenant → 401.
3. Key without required scope → 403 `forbidden_scope`.
4. Lookup is constant-time on the hash; secret never appears in logs (T-11 grep test).
5. Revocation takes effect within 60 s (cache TTL) and immediately if the revoke endpoint is used (cache delete).

### US-06 Enforce rate limits and concurrency
As Ops, I want per-key request limits and per-tenant concurrent-stream limits, so that
one tenant cannot degrade others. (F-07, NFR-05)

Acceptance criteria:
1. Exceeding `req_per_minute` → 429 `rate_limited` with `Retry-After` and `X-RateLimit-*` headers.
2. Exceeding `max_concurrent_streams` → 429 `concurrency_limited`.
3. Leases expire automatically if the gateway crashes (TTL 60 s, renewed per chunk).
4. Limiter is atomic under concurrent requests (T-05 hammers 100 goroutines).

### US-07 Enforce monthly character quota
As Ops, I want quotas enforced per plan, so that free-tier abuse is bounded. (F-07)

Acceptance criteria:
1. Request that would exceed `chars_per_month × 1.05` → 402 `quota_exceeded`.
2. Counter increments only after successful synthesis (or partial on cancel).
3. Hourly reconcile corrects Redis from `usage_daily` (T-06).

### US-08 Synchronous synthesis endpoint
As Dev, I want `POST /v1/synthesize` to return a complete audio file, so that I can
integrate with one HTTP call. (F-01, F-05, NFR-02)

Acceptance criteria:
1. Returns `audio/wav`, `audio/mpeg` or `audio/ogg` per `format`, with `X-Request-Id`, `X-Cache`, `X-Audio-Duration-Ms`, `X-Chars-Billed`.
2. `normalize=true` by default; `false` passes text verbatim.
3. Unknown voice → 422 `unknown_voice`; text > 3000 → 413 `text_too_long`.
4. Errors are `application/problem+json` with stable `code`.
5. Sync latency p95 ≤ 0.7 × duration + 200 ms in load test.

### US-09 Streaming synthesis over HTTP chunked
As Bot, I want audio streamed as it is generated, so that my users hear the first word
within 300 ms. (F-02, NFR-01)

Acceptance criteria:
1. First byte of audio (after RIFF header) sent ≤ 300 ms p95 at 80% utilization.
2. Client disconnect cancels the worker call within 200 ms (T-07).
3. Chunks arrive in order; a corrupted or out-of-order frame aborts the stream with a logged error.
4. Stream idle > 30 s (worker stalled) → connection closed, slot released.

### US-10 Streaming over WebSocket with cancel
As Bot, I want a WebSocket variant with an explicit cancel message, so that barge-in
works without tearing down the socket. (F-02)

Acceptance criteria:
1. First text frame is the JSON request; binary frames carry PCM/Opus; `{"type":"end"}` closes the utterance.
2. `{"type":"cancel"}` stops the current utterance within 200 ms and the socket stays open for the next request.
3. Concurrency lease is held for the socket lifetime.

### US-11 Audio cache
As Ops, I want deterministic caching so that repeated phrases cost no CPU. (F-08, ADR-006)

Acceptance criteria:
1. Two requests with identical normalized text/voice/params/model → second is `X-Cache: HIT` and touches no worker (T-08).
2. `"0.8"` and `"0.80"` temperature, and whitespace variants of text, hit the same key.
3. Different model version → miss.
4. Cache hit still increments quota and usage with `cached=true`.
5. Eviction job removes entries idle > 30 days and their S3 objects.

### US-12 Admission control under overload
As Ops, I want overload rejected fast with `Retry-After`, so that the service never
accept-then-times-out. (NFR-05, ADR-007)

Acceptance criteria:
1. At 3× capacity, p95 time to a 503 response < 50 ms (k6 scenario).
2. Stream requests still get slots while batch is saturated (30% reservation).
3. `Retry-After` reflects queue depth × mean service time, bounded 1–30 s.

### US-13 Voice catalogue
As Dev, I want to list voices with tags and preview clips. (F-04)

Acceptance criteria:
1. `GET /v1/voices` works with or without a key.
2. Tenant-scoped voices appear only for their tenant.
3. Preview URLs are public, cached, and generated at deploy from a fixed sentence.

## Epic E3 — Jobs and usage

### US-14 Create and track long-text jobs
As Dev, I want to submit up to 100k characters and poll a job, so that I can generate
podcasts and lessons. (F-03, F-10, FL-03)

Acceptance criteria:
1. `POST /v1/jobs` requires `Idempotency-Key`; repeat with same body → same job (202); same key, different body → 409.
2. Status progresses `queued → segmenting → synthesizing → merging → completed` and exposes `segments_done/total`.
3. A failing segment retries 3× with backoff; job `failed` after that with `error` populated.
4. Completed job exposes `output_url` valid 24 h; input text is deleted from S3 within 48 h.
5. Cancel sets `cancelled` and no further segments are dispatched.
6. Gateway restart mid-job resumes without re-synthesizing `done` segments (T-09).

### US-15 Job completion webhook
As Dev, I want an HMAC-signed webhook when a job finishes. (F-11)

Acceptance criteria:
1. POST with `X-ViTTS-Signature: sha256=<hex>` over the raw body and `X-ViTTS-Timestamp`.
2. Retries 5× with exponential backoff on non-2xx; attempts recorded.
3. URLs resolving to private/loopback ranges are rejected at job creation (422).

### US-16 Usage report
As Dev, I want daily usage in JSON and CSV. (F-09)

Acceptance criteria:
1. `GET /v1/usage?from&to` returns per-day chars, audio_ms, requests, cache_hits and period totals vs plan limit.
2. Data lags real time by ≤ 5 min (rollup cadence).
3. CSV has a header row and RFC 4180 quoting.

### US-17 API key management
As Dev, I want to create, list and revoke keys with scopes. (F-06)

Acceptance criteria:
1. Create returns the secret exactly once; list shows prefix only.
2. Revoke → 204; subsequent use → 401 immediately.
3. Every create/revoke writes `audit_log`.

## Epic E4 — Operations

### US-18 Observability
As Ops, I want metrics, traces and structured logs. (NFR-08)

Acceptance criteria:
1. `/metrics` exposes RED metrics per route plus `tts_ttfa_seconds`, `tts_rtf`, `dispatch_queue_depth{class}`, `dispatch_queue_wait_seconds`, `cache_hit_total`, `usage_chars_total{tenant}`, `worker_slots_busy`.
2. A request trace spans HTTP → dispatch → gRPC → worker.
3. Logs are JSON with `request_id`, `tenant_id`; no text, no secrets.
4. Grafana dashboard JSON committed in `deploy/grafana/`.

### US-19 Local stack and CI
As Ops, I want `docker compose up` to run everything and CI to gate merges. (NFR-10)

Acceptance criteria:
1. Compose starts gateway, 1 worker, Postgres, Redis, MinIO, Prometheus, Grafana; `make smoke` passes.
2. CI runs lint, unit, integration (testcontainers), contract regeneration check, `govulncheck`, `pip-audit`.
3. Images are multi-stage, non-root, pinned base digests.

### US-20 Load test and capacity report
As Ops, I want a repeatable k6 load test proving NFR-01/02/05. (NFR-03)

Acceptance criteria:
1. `scripts/loadtest/` scenarios: steady 80%, spike 3×, cache-heavy.
2. Report includes TTFA p50/p95/p99, RTF, rejection latency, cache hit ratio.
3. Results committed to `docs/reports/` per release.
