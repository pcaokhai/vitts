# M3 runbook drills

Task 3.4. Every procedure in `13-runbook.md` was executed against the local stack on an
Apple M4 Pro rather than read. Four of the five found a defect — two of them critical — which is
the argument for drilling a runbook instead of reviewing it.

## What was drilled

| Drill | Runbook procedure | Result |
|-------|-------------------|--------|
| 1 | Replace a worker node | Passed after finding 1 was fixed |
| 2 | Redis lost and restarted | **Failed**: findings 1 and 2 |
| 3 | Postgres restore (T-20) | Passed, and found finding 3 |
| 4 | Gateway boots against a stale schema | Added as a result of finding 3 |
| 5 | `make smoke` end to end | **Failed**: findings 5 and 6, plus two stale assertions in the script itself |

Findings 1 and 5 are both "a dependency moved and the gateway never noticed". Neither was
reachable from a test that starts everything healthy and keeps it that way, which is the
gap worth closing next.

## Finding 1 — a Redis restart broke every request until the gateway was restarted

Severity: critical. Fixed in this task.

The limiter loaded its three Lua scripts with `SCRIPT LOAD` at boot and called `EVALSHA`
with the returned SHAs. A Redis restart empties the script cache, so every later call
answered `NOSCRIPT No matching script`, which the middleware turned into `500 internal` —
for every tenant, on every request, until someone restarted the gateway.

```
ERR rate limiter unavailable error="token bucket: NOSCRIPT No matching script. Please use EVAL."
ERR request failed code=internal status=500
```

This is the exact scenario the runbook's "Redis lost" row claims the service survives.
Nothing in CI covered it: the integration tests start a fresh Redis and never restart it.

Fix: `goredis.Script.Run`, which sends `EVALSHA` and retries with the script body on
`NOSCRIPT`. Boot still preloads, so the hot path is unchanged. Regression test T-33
flushes the script cache mid-test and fails on the old code with the error above.

## Finding 2 — the gateway failed closed where the runbook promised bounded fail-open

Severity: major. Fixed in this task, recorded as ADR-011.

With Redis down, `/v1/synthesize` returned `500 internal`. The runbook had said since it
was written that the limiter "fails open for 60 s with warning log". Neither side had been
reconciled: the code comment argued fail-closed, the doc specified otherwise, and both had
been reviewed.

The doc was right, and the code was a single point of failure against a 99.5% target
(NFR-04). ADR-011 sets the rule: 60 s of continuous failure is served and counted, after
which requests are refused with `503 overloaded` + `Retry-After` — not `500`, because a
dependency outage has a recovery time. Quota behaves the same way, and usage is still
recorded after delivery, so a tenant served while degraded is billed once Redis returns.

Verified after the fix:

```
baseline synth: 200
Redis down, inside degrade window: 200
Redis back (script cache empty): 200
dependency_degraded_total{dependency="quota"} 1
dependency_degraded_total{dependency="ratelimit"} 1
```

## Finding 3 — the migrations job reported success while a milestone of schema was missing

Severity: major. Fixed in this task.

The restore drill compared `goose_db_version` between the live database and the restored
copy and found both at version 1, with two migrations in the tree. `jobs`, `job_segments`
and `usage_daily` did not exist in the live stack at all.

Cause: `docker compose up -d` reuses a cached image. The migrate image had been built at
`2026-09-19T02:24`; `00002_jobs_and_usage.sql` landed at `21:55` the same day. The job ran
the migrations embedded in the *old* binary, applied nothing, logged
`no migrations to run. current version: 1`, and exited 0. Compose reported
`service_completed_successfully`, so the gateway started against a schema missing every
M2 table.

This is a dev-stack accident, but the production shape is the same and worse: the
migrations job and the gateway are deployed as separate containers, so a rollout that
updates one and not the other produces a gateway querying columns that do not exist —
discovered by the first customer request, not by the deploy.

Fix, in three places:

- `migrations.Latest()` reads the highest version embedded in the binary, and the gateway
  compares it with `goose_db_version` at boot. A gateway ahead of its schema refuses to
  start, naming both versions. Verified in drill 4:
  `gateway: database is at schema version 1 but this build needs 2: run the migrations job for this image before starting the gateway`
- `make up` now passes `--build`.
- The runbook's deploy section states that the job and the gateway must be the same tag,
  and what the refusal looks like.

## Finding 4 — the runbook's alert table named metrics that do not exist

Severity: minor. Fixed in this task.

Four of seven rows referenced signals nothing emits: `worker_orphan_inference_total`,
`jobs_pending_age_seconds`, `redis_uptime_seconds` and a `job_overrun` alert. An operator
following the table at 3am would have queried empty series and concluded the system was
healthy.

The table is now generated from `deploy/prometheus/alerts.yml` by hand-matching each
alert name, and says to trust the rules file over the table. The Redis row is replaced by
`ServingDegraded`, which alerts on `dependency_degraded_total` — the impact — rather than
on Redis's uptime, so it needs no Redis exporter and cannot be green while the gateway
serves unmetered traffic.

## Finding 5 — jobs queued for minutes beside a completely idle worker

Severity: critical. Fixed in this task.

`make smoke` was still printing "sync, stream and job legs land in tasks 1.9, 1.10 and
2.3" a milestone after those tasks landed, so it had been asserting nothing about the
gateway. Filling it in (sync, stream and job through a throwaway tenant) failed on the
job leg immediately: the first job after a gateway restart completed in seconds, and
every job after that sat in `queued` or `segmenting` until the smoke timeout.

The first reading was head-of-line blocking — one job holding the stream for its whole
`BatchWait` budget. The metrics said otherwise:

```
dispatch_queue_depth{class="batch"} 1
worker_slots_busy{worker="worker:50051"} 0
worker slots=0/1          (the worker's own view)
```

A caller was queued for a slot that was free, and the gateway knew it was free. Not
contention — a missed wakeup.

`Dispatcher.Reserve` parks on `d.free`, an edge signal sent by `releaseSlot`. But free
capacity is *derived from health snapshots* the pool refreshes on its own schedule. Any
slot that frees without passing through `releaseSlot` — a worker re-admitted after
ejection, a request the worker abandoned, a snapshot that simply catches up — moves the
capacity number with no signal behind it. A waiter already parked then sleeps until its
class budget expires: 5 minutes for a batch job, beside an idle worker.

The ejection in drill 2 is what set it up. Redis' DNS failure coincided with three failed
health polls, the worker was ejected and re-admitted, and from then on the dispatcher's
signal and its capacity disagreed for every subsequent request.

Fix: a waiter re-reads capacity at least every `recheckInterval` (50 ms) instead of
trusting the edge alone. Correctness no longer depends on every capacity change producing
a signal — the signal is now an optimisation that makes the common case immediate.

The retry needed a second guard: re-checking must not let a caller take a slot after its
budget has passed, which would be accept-then-timeout (NFR-05) arriving by the back door.
Every attempt after the first is now inside the budget or refused — caught by
`TestBatchCannotConsumeTheStreamReservation` failing on the first version of the fix.

T-109 parks a waiter against a fleet reported busy, then reports it free without any
release. It fails on the old code by sleeping the full `StreamWait` (2.04 s), and passes
in milliseconds on the new. Two consecutive `make smoke` runs now pass, including the
second, which is the one that used to stall.

## Finding 6 — a job stuck in `segmenting` could never recover

Severity: major. Fixed in this task.

`handleJob` moves a job `queued -> segmenting`, then writes the segment rows. An
orchestrator that dies in between leaves the job in `segmenting`. The reconciler spots it
and re-queues it — correctly — but `handleJob` then reads a status that is neither
`queued` nor a resumable `synthesizing`/`merging`, concludes "another orchestrator took
it", and returns. The job is re-queued and dropped once a minute, forever, with no error
anywhere:

```
WRN stalled job re-queued job_id=d106ccd1-... status=segmenting
WRN stalled job re-queued job_id=d106ccd1-... status=segmenting
WRN stalled job re-queued job_id=d106ccd1-... status=segmenting
```

The customer sees a job that never finishes and never fails. Only the reconciler's warning
distinguishes it from a slow job, and nothing alerts on it.

Fix: `segmenting` is resumable. Segment inserts are already `on conflict (job_id, seq) do
nothing`, so re-running segmentation is idempotent, which is why this is a three-line
change rather than a state-machine redesign. T-108 covers it and fails on the old code
with `expected "completed", actual "segmenting"`.

## Drill 1 — replace a worker node

Passed. With the only worker stopped, `/readyz` reported `503` and named the failing
dependency, and synthesis returned `503 overloaded` with `Retry-After: 1` — the ADR-007
behaviour, not a 500. (The 500 seen on the first attempt was finding 1, not a dispatch
bug.) On restart the worker was healthy in 6 s reusing the weights volume, `/readyz`
returned 200 with no gateway restart, and synthesis resumed at 200.

`VITTS_WORKER_ADDRS` still requires a gateway restart to add a *new* address, as the
runbook says. That is acceptable at this fleet size and is the next thing to change if
the fleet grows.

## Drill 3 — Postgres restore, T-20

Passed.

```
before dump:   tenants=4 keys=4
dump:          84511 bytes (pg_dump -Fc)
after restore: tenants=4 keys=4
migrations:    OK 00002_jobs_and_usage.sql; re-run -> "no migrations to run"
partitions:    3 synth_requests partitions + ensure_synth_requests_partition() restored
```

Migrations are idempotent against a restored dump, and the partitioned table and its
helper function survive `pg_restore` without manual repair. The runbook now carries the
exact commands rather than a summary.

## Not drilled

- **Rotate admin key** — a config change with no failure mode worth staging; covered by
  the boot validation in `internal/config`.
- **Model upgrade** — needs a second model revision to be meaningful. Worth drilling when
  one exists, since it is the procedure most likely to surprise (cache keys change, so
  every tenant misses at once).
