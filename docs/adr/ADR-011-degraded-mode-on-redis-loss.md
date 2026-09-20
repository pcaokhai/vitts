# ADR-011: Redis loss degrades for a bounded window, then refuses

Status: Accepted

Relates to NFR-04 (99.5% availability) and US-06/US-07, which define the limits Redis
enforces. It does not change what the limits are, only what happens when they cannot be
read.

## Context

Every tenant request consults Redis twice: a token bucket for the per-key rate limit
(US-06) and a monthly counter for quota (US-07). Redis is also the cache index and the
job queue, but those already degrade on their own — a cache miss synthesizes, and jobs
are asynchronous.

The gateway shipped failing closed on both: an unreadable limiter returned 500. The
reasoning in the code was "a limiter we cannot consult is not permission to ignore the
limit". That reasoning is sound about abuse and wrong about availability: it makes a
single-node Redis a single point of failure for an API with a 99.5% monthly target, where
a 30-second restart becomes a 30-second total outage.

`13-runbook.md` had specified the opposite behaviour since the doc set was written —
"service keeps serving … limiter fails open for 60 s with warning log" — and nobody had
reconciled the two. The M3 runbook drill (`docs/reports/drill-m3.md`) is what surfaced
the disagreement.

Failing open without a bound is not acceptable either: a Redis that stays down overnight
would serve unlimited, unmetered traffic to every tenant, and the first anyone would know
is the invoice.

## Decision

A dependency that only *gates* a request may be unavailable for a bounded window. Within
the window the request is served; past it, requests that need that dependency are refused.

- Window: 60 s of *continuous* failure, per `internal/degrade`. Any success resets it, so
  repeated blips do not accumulate into a refusal.
- Rate limiting: inside the window the request proceeds unlimited and logs a warning.
  Past it, `503 overloaded` with `Retry-After`, not `500 internal` — this is a dependency
  outage with a recovery time, and the client should come back rather than treat it as a
  bug.
- Quota: inside the window the check passes. Usage is still *recorded* after delivery, so
  a tenant served during the window is billed as soon as the counter returns; the tenant
  may overshoot its allowance by at most one window's traffic.
- Every degraded request increments `dependency_degraded_total{dependency}`. The alert
  fires on this, not on Redis's own uptime: the metric measures the impact, exists whether
  or not a Redis exporter is deployed, and cannot be green while the gateway is serving
  unmetered.

Authentication is deliberately not in scope: keys are read from Postgres, so a Redis
outage never weakens authentication or tenant isolation.

## Consequences

- A Redis restart is invisible to callers instead of a full outage.
- A tenant can exceed its rate limit, and overshoot its quota, for at most the window.
  This is a deliberate, bounded, metered loss — and the bill still lands.
- A Redis outage longer than a minute is a partial outage by design. That is the point:
  the gateway stops rather than silently serving free, unlimited traffic.
- The window is one constant. If an operator wants stricter behaviour, it is one value,
  not a redesign.
