# PRD — ViTTS Gateway

## 1. Summary

ViTTS Gateway is a hosted API that turns Vietnamese text into natural speech using the
open ZeroTTS model, priced per character, with sub-300 ms streaming and no GPU. It lets
Vietnamese developers add high-quality Vietnamese voice to their apps without operating an
ML model themselves.

## 2. Contacts

| Name | Role | Comment |
|------|------|---------|
| Khai Phan | Product owner, lead engineer | Owns scope, architecture, pricing |
| Claude Code | Implementation agent | Works from this doc set; escalates ambiguity via issues |
| zeroweight.ai | Model vendor (upstream OSS) | Contact for custom voice latents |

## 3. Background

Vietnamese TTS from hyperscalers is priced around $16 per million characters and has a
limited set of Vietnamese voices. Open models until recently were either slow on CPU or
had word error rates above 15%. ZeroTTS (2026, MIT license) changes this: 1.03% WER on
ZeroBench-TTS, real-time factor 0.5× on a laptop CPU, first audio in ~70 ms, and 8 preset
voices. Zero-shot cloning is advertised but the voice encoder is not published, so the
product ships preset voices only.

Why now: the model is good enough and cheap enough to run on commodity CPU, which makes
a small, profitable API business viable for a single engineer, and the wrapper itself
(streaming, backpressure, metering, multi-tenancy) is a strong demonstration of
production backend engineering.

## 4. Objective

Build a production-grade TTS API that a developer can integrate in under 15 minutes and
that stays profitable on cheap CPU infrastructure.

Key results (first 90 days after public launch):

| KR | Target |
|----|--------|
| KR1 | p95 time-to-first-audio on streaming endpoint ≤ 300 ms at 80% worker utilization |
| KR2 | 99.5% monthly availability of `/v1/synthesize*` measured by external probe |
| KR3 | 20 tenants with at least one successful paid request; 3 on Pro plan |
| KR4 | Infrastructure cost ≤ 40% of monthly revenue by day 90 |
| KR5 | Zero incidents of cross-tenant data exposure (verified by integration tests in CI) |

## 5. Market segments

Defined by job-to-be-done, not demographics.

| Segment | Job | Constraint |
|---------|-----|------------|
| Voice chatbot builders | Speak LLM replies in real time | Needs streaming, cancel on barge-in, concurrency |
| Content/news apps | Read articles aloud, generate podcasts | Needs long-text jobs, MP3 output, cheap per character |
| Fintech/IoT (soundbox, IVR) | Announce fixed phrases with variable numbers | Needs low latency, high cache hit, 8/16 kHz output |
| E-learning | Generate lesson audio in bulk | Needs batch jobs, webhooks, predictable cost |

Out of scope for v1: custom voice cloning, on-prem deployment, SSML.

## 6. Value proposition

| Customer need | What they gain | Pain avoided |
|---------------|----------------|--------------|
| Accurate Vietnamese incl. dates, numbers, English words | 4× fewer word errors than other open models | Manual text rewriting before synthesis |
| Low latency | First audio in < 300 ms | Awkward pauses in voice bots |
| Simple pricing | Per-character, pay only for what you use | Opaque per-minute or seat pricing |
| No ML ops | One HTTP call | Managing 900 MB weights, ONNX runtime, CPU tuning |

## 7. Solution

### 7.1 UX and flows

Developer signs up on console → creates API key → calls `/v1/synthesize` with text and
voice → receives audio. For long text, creates a job and polls or receives a webhook.
Console shows usage by day and lets the tenant revoke keys. See `06-flows.md`.

### 7.2 Key features

| ID | Feature | Priority |
|----|---------|----------|
| F-01 | Synchronous synthesis returning WAV/MP3/OGG | Must |
| F-02 | Streaming synthesis over HTTP chunked and WebSocket | Must |
| F-03 | Async jobs for text up to 100k characters with segment retry and merge | Must |
| F-04 | Voice catalogue with tags and preview clips | Must |
| F-05 | Vietnamese text normalization (on by default, toggleable) | Must |
| F-06 | Tenant API keys with scopes, prefix display, revocation | Must |
| F-07 | Plan quotas (chars/month), per-key rate limits, per-tenant concurrency limits | Must |
| F-08 | Deterministic audio cache keyed by text + voice + params + model version | Must |
| F-09 | Usage ledger, daily aggregates, CSV export | Must |
| F-10 | Idempotency keys on job creation | Must |
| F-11 | Webhook on job completion, HMAC-signed | Should |
| F-12 | Minimal web console (keys, usage, docs) | Should |
| F-13 | Output resampling (8/16/24/48 kHz) | Should |
| F-14 | Python and JS SDKs | Could |
| F-15 | Tenant-scoped custom voice latents (when vendor provides `.npz`) | Later |

### 7.3 Non-functional requirements

| ID | Requirement | Target |
|----|-------------|--------|
| NFR-01 | Streaming TTFA | p95 ≤ 300 ms, p99 ≤ 600 ms |
| NFR-02 | Sync latency | p95 ≤ 0.7 × audio duration + 200 ms |
| NFR-03 | Throughput | ≥ 2 h audio per hour per 8-vCPU worker node |
| NFR-04 | Availability | 99.5% monthly |
| NFR-05 | Overload behaviour | Reject within 50 ms with 429/503 + `Retry-After`; never accept-then-timeout |
| NFR-06 | Ordering | Audio chunks strictly ordered; job segments merged in sequence |
| NFR-07 | Security | TLS 1.2+, hashed keys, tenant isolation on every query, no PII in logs |
| NFR-08 | Observability | RTF, TTFA, queue depth, cache hit ratio, chars/tenant; distributed traces |
| NFR-09 | Cost | Infra ≤ 40% of revenue at steady state |
| NFR-10 | Portability | Full stack runs via docker compose on one machine |
| NFR-11 | Data retention | No request text stored by default; audio cache evicted after 30 days idle |
| NFR-12 | Recovery | Worker restart without re-downloading weights; RTO 15 min, RPO 1 h for Postgres |

### 7.4 Assumptions (to validate)

| ID | Assumption | Validation |
|----|------------|------------|
| A1 | RTF ≈ 0.5 holds with 8 threads on Hetzner CCX / AWS c7i CPUs | **Partly confirmed (0.5):** worst RTF 0.32× on Apple M4 Pro, `docs/reports/bench-m0.md`. Re-run `make bench` on Hetzner/AWS before treating it as settled there. |
| A2 | Two 4-thread processes per 8-vCPU node still beat real time | **Confirmed (0.5):** worst RTF 0.37× with 2×4 threads, `docs/reports/bench-m0.md` |
| A3 | Developers accept preset-only voices at launch | Landing page survey, first 10 interviews |
| A4 | Cache hit ratio ≥ 50% for IVR/soundbox tenants | Measure in first 30 days |
| A5 | $6–8 per million characters clears willingness-to-pay | Pricing page test, sales calls |
| A6 | Upstream keeps MIT license and publishes stable weights | Pin version, mirror weights |

## 8. Release

| Phase | Scope | Relative timing |
|-------|-------|-----------------|
| M0 Spike | Benchmark, proto, worker skeleton | Week 1–2 |
| M1 Core API | F-01, F-02, F-04, F-05, F-06, F-07, F-08 | Week 3–5 |
| M2 Jobs | F-03, F-09, F-10, F-11 | Week 6–7 |
| M3 Launch | F-12, observability, runbook, load test, private beta | Week 8–9 |
| M4 Growth | F-13, F-14, pricing iteration | Post-launch |

See `12-implementation-plan.md` for task-level detail.
