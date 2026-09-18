# Pre-mortem — ViTTS Gateway

Imagine the private beta launched two weeks ago and it is failing. Why?

## Tigers (real risks)

| Risk | Evidence | Class | Mitigation | Owner |
|------|----------|-------|------------|-------|
| TTFA collapses under mixed load because batch jobs consume all slots | Single core pool per node; upstream numbers are single-request | Launch-blocking | ADR-007 reservation + k6 spike test (T-13/T-14) before beta | Eng |
| Cancelled streams keep burning CPU | Python inference loop must poll cancellation; easy to miss | Launch-blocking | T-02/T-07 in CI; metric `worker_orphan_inference_total` alert > 0 | Eng |
| Cold start: weights 900 MB, worker restart takes minutes and readiness lies | Boot path downloads from HF | Launch-blocking | S3 mirror, warm-up gate, `/readyz` depends on ≥1 ready worker | Ops |
| Quota drift after Redis restart lets free tier run unlimited | Redis is a cache of truth | Fast-follow | AOF + hourly reconcile (T-06), alert on Redis restart | Eng |
| First customers ask for custom voices and churn | Cloning not published upstream | Fast-follow | Say it on pricing page; open vendor conversation in M2; F-15 schema ready | Product |
| One tenant floods jobs and starves others | Batch class shares queue | Track | Per-tenant fair queue in `jobs:segments` consumer; plan `max_job_chars` | Eng |
| Hetzner/VPS node dies with no replica | Single worker node in MVP | Track | Second node when revenue > $200/mo; runbook has rebuild in 30 min | Ops |

## Paper tigers (overblown)

| Concern | Why it is not a real risk now |
|---------|-------------------------------|
| "Need Kafka for reliability" | Volume is hundreds of jobs per minute at most; Redis Streams + Postgres record is sufficient (ADR-005) |
| "Cache lets tenants see each other's audio" | Audio is a deterministic function of public inputs; no tenant data is in the cache value |
| "Go is the wrong choice, should be Java" | Contract-first design makes a Java gateway a drop-in later (ADR-001) |
| "Need multi-region for latency" | Target users are in Vietnam; one region in Singapore/HCMC is fine |

## Elephants (unspoken, need investigation)

| Elephant | Investigation |
|----------|---------------|
| Actual RTF on the cheap CPUs we plan to buy (Hetzner AMD EPYC vs AWS Sapphire Rapids) may differ 2× from the README laptop | M0 benchmark on the real target; publish numbers in `docs/reports/` |
| Vietnamese Decree 13/2023 personal data obligations if tenants send PII (names, amounts) | Legal review of ToS; ADR-008 default no-storage; DPA template |
| Deepfake misuse once cloning arrives | Watermarking research; ToS; identity verification for cloning tier |
| Willingness to pay at $6–8/M chars for Vietnamese-only | 10 customer interviews during beta |

## Action plans for launch-blocking tigers

1. Slot reservation and overload rejection: implement in M1, prove in M3 load test; decision gate before beta invites.
2. Cancellation: implement cooperative cancellation in `engine.py` (check `context.is_active()` per frame); CI test T-02/T-07; alert.
3. Cold start: implement mirror + warm-up in M0; measure restart-to-ready < 90 s.
