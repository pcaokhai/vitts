# ADR-007: Reject fast under overload; reserve slots for streaming

Status: Accepted

## Context
Accepting requests we cannot serve within their latency budget wastes CPU and produces
timeouts that look like outages (NFR-05). Batch jobs could starve interactive streams.

## Decision
The dispatcher holds a bounded queue per priority class (`stream` > `sync` > `batch`).
Queue wait timeout: stream 2 s, sync 10 s, batch unbounded but yields to higher classes.
30% of slots are reserved for `stream`. On timeout the gateway returns 503 with
`Retry-After` computed from queue depth × mean service time.

## Alternatives considered
| Option | Pros | Cons |
|--------|------|------|
| Unbounded queue | Never rejects | Accept-then-timeout, cascading failure |
| Per-tenant fair queue only | Fairness | Does not protect latency class |

## Consequences
Clients must implement retry with jitter (documented in API). Load tests must prove p95
rejection latency < 50 ms under 3× capacity.
