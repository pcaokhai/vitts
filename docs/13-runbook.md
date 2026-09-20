# Runbook

## Deploy

- `deploy/docker-compose.prod.yml`, images tagged by git SHA, pulled from GHCR.
- Order: migrations job → worker (wait `/readyz` on gateway shows ≥1 ready) → gateway
  rolling (2 replicas, one at a time, 30 s drain).
- The migrations job and the gateway must be the **same image tag**. A job image older
  than the gateway applies fewer migrations and still exits 0; the gateway then refuses to
  boot with `database is at schema version N but this build needs M`, which is the
  intended failure. Locally, `make up` builds before starting for the same reason — a
  plain `docker compose up -d` reuses a stale image silently.
- The console deploys independently and after the gateway. It is a pure API client
  (ADR-012), so it can never break the API — but the API can break it: a gateway rolled
  out without `VITTS_CONSOLE_ORIGIN` leaves the console loading and then failing every
  request with a CORS error the browser reports and the gateway does not log.
- Rollback: redeploy previous SHA; migrations are forward-only, so schema changes must be
  backward compatible for one release (expand/contract pattern).

## Dashboards and alerts

Rules are `deploy/prometheus/alerts.yml`; the dashboard is `deploy/grafana/dashboards/`.
Every row below names a metric the gateway actually emits — check the rules file, not this
table, when writing a new alert.

| Alert | Severity | First action |
|-------|----------|--------------|
| `StreamingTTFATooSlow` | page | Check `worker_slots_busy` and `dispatch_queue_depth`; add a worker or shed batch traffic |
| `QueueWaitTooLong` | page | Same as above; verify the stream reservation percentage (ADR-007) |
| `RealTimeFactorAboveOne` | page | The worker is slower than real time: check CPU contention and `ORT_INTRA_OP_THREADS` |
| `NoReadyWorker` | page | Restart the container; check the weights volume and RAM |
| `GatewayErrorRateHigh` | page | Read `code=` in the gateway logs; 5xx is never a client's fault |
| `ServingDegraded` | page | Redis is unreachable and traffic is unmetered — see **Redis lost** |
| `OverloadRejectionsSustained` | ticket | Capacity decision: add workers or revisit plan limits |
| `GatewayDown` | page | Check the container and its dependencies; `/healthz` is liveness only |
| `JobFailureRateHigh` | page | Check segment errors and `XPENDING`; look for one poisoned segment |
| `WebhookDeliveryFailing` | ticket | Usually the receiver. Check the SSRF guard rejected nothing legitimate |

Not alerted on, deliberately: Redis's own uptime. `ServingDegraded` measures the impact
instead, so it works without a Redis exporter and cannot be green while the gateway is
serving unmetered traffic (ADR-011).

## Common procedures

**Replace a worker node**: provision, mount weights volume or let mirror pull, start
container, confirm `Health.ready`, add address to `VITTS_WORKER_ADDRS`, restart gateway
(rolling). Remove old address, drain, stop.

**Redis lost**: the service keeps serving. Authentication is unaffected — keys live in
Postgres. Rate limiting and quota degrade for 60 s of continuous failure with a warning
log and `dependency_degraded_total`, then refuse with `503 overloaded` + `Retry-After`
(ADR-011). The cache simply misses. Restore from AOF and restart Redis; nothing needs to
be run by hand — the quota reconciler runs inside the gateway process and repairs the
counters from the usage ledger on its next pass. Expect a tenant to have overshot its
limits by at most one window's traffic; the usage ledger still has the characters, so the
bill is correct.

A Redis restart empties its Lua script cache. The gateway re-sends the script bodies on
`NOSCRIPT`, so no gateway restart is needed — verified in the M3 drill, which is where
this was found *not* to be true (`docs/reports/drill-m3.md`).

**Postgres restore** (drilled, T-20):

```
pg_dump  -U vitts -d vitts -Fc -f nightly.dump          # nightly, offsite
createdb -U vitts vitts_restore
pg_restore -U vitts -d vitts_restore --no-owner nightly.dump
gateway -migrate                                         # idempotent; no-op if current
```

Then point `VITTS_DATABASE_URL` at the restored instance and restart the gateway. The
`synth_requests` partitions and `ensure_synth_requests_partition()` come back with the
dump — no partition needs recreating. Expect ≤ 1 h data loss (RPO, NFR-12).

**Rotate admin key**: set new value, restart gateway, update operator tooling, audit
log the rotation.

**Model upgrade**: bump `VITTS_MODEL_REVISION`, deploy workers one by one; cache misses
for new version are expected; watch WER harness before flipping all workers.

## Scheduled jobs

See `06-flows.md` FL-07. Each job logs start and finish with a duration. There is no
`job_overrun` alert: the scheduler's lock already prevents overlap, and `JobFailureRateHigh`
covers jobs that fail rather than run long. Add one when a scheduled job has actually
overrun in production, so the threshold comes from a real duration.

## Verifying a deploy by hand

`docs/manual-tests/` walks every feature with its pass criterion, in twelve numbered
files. After a release, files 2 (health), 4 (limits) and 10 (resilience drills) are the
ones worth repeating: they cover what breaks quietly rather than loudly.

## Incident template

Title, impact (tenants, duration), timeline (UTC+7), root cause, what stopped it,
follow-ups with owners. Stored in `docs/incidents/YYYY-MM-DD-slug.md`.
