# Runbook

## Deploy

- `deploy/docker-compose.prod.yml`, images tagged by git SHA, pulled from GHCR.
- Order: migrations job → worker (wait `/readyz` on gateway shows ≥1 ready) → gateway
  rolling (2 replicas, one at a time, 30 s drain).
- Rollback: redeploy previous SHA; migrations are forward-only, so schema changes must be
  backward compatible for one release (expand/contract pattern).

## Dashboards and alerts

| Alert | Condition | Severity | First action |
|-------|-----------|----------|--------------|
| TTFA high | `histogram_quantile(0.95, tts_ttfa_seconds) > 0.3` for 5 m | page | Check `worker_slots_busy`, add worker or shed batch |
| Overload | `rate(http_requests_total{status="503"}[5m]) > 5%` | page | Same as above; verify reservation pct |
| Worker down | `worker_ready == 0` | page | Restart container; check weights volume and RAM |
| Orphan inference | `worker_orphan_inference_total > 0` | ticket | Cancellation bug; capture trace |
| Queue stuck | `jobs_pending_age_seconds > 600` | page | Reconciler logs; XPENDING; restart orchestrator |
| Redis restarted | `redis_uptime_seconds < 300` | ticket | Run quota reconcile manually |
| Disk | S3 errors or Postgres disk > 80% | ticket | Eviction job; partition drop |

## Common procedures

**Replace a worker node**: provision, mount weights volume or let mirror pull, start
container, confirm `Health.ready`, add address to `VITTS_WORKER_ADDRS`, restart gateway
(rolling). Remove old address, drain, stop.

**Redis lost**: service keeps serving (auth falls back to Postgres, limiter fails open
for 60 s with warning log, cache misses). Restore from AOF; run `make reconcile-quota`.

**Postgres restore**: `pg_restore` latest nightly dump into new instance, run migrations
(idempotent), point `VITTS_DATABASE_URL`, restart gateway. Expect ≤ 1 h data loss (RPO).

**Rotate admin key**: set new value, restart gateway, update operator tooling, audit
log the rotation.

**Model upgrade**: bump `VITTS_MODEL_REVISION`, deploy workers one by one; cache misses
for new version are expected; watch WER harness before flipping all workers.

## Scheduled jobs

See `06-flows.md` FL-07. Each job logs start/finish with duration; missing finish within
2× expected duration raises `job_overrun` alert.

## Incident template

Title, impact (tenants, duration), timeline (UTC+7), root cause, what stopped it,
follow-ups with owners. Stored in `docs/incidents/YYYY-MM-DD-slug.md`.
