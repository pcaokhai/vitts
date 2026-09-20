# Load test M3 — capacity and SLO evidence

Scenarios: `scripts/loadtest/{steady,spike,cache}.js`. Re-run with
`VITTS_KEY=<tenant secret> make loadtest VITTS_SCENARIO=<name>`.

Machine: MacBook Pro M4 Pro, 12 cores. **One worker, one inference slot** (ADR-003),
gateway and worker both in Docker Desktop. A production fleet scales these numbers with
the worker count; the ratios are what transfer, not the absolute rates.

## Measured capacity

| Arrival rate | Rejections in 60 s | Verdict |
|---|---|---|
| 1 request / 2 s (0.50 rps) | 11 of 31 | above capacity |
| 1 request / 3 s (0.33 rps) | 2 | at the edge |
| **1 request / 4 s (0.25 rps)** | **0** | sustainable |

The corpus mixes utterances of 28 to 97 characters, which synthesize in roughly 1.5 to
3 s at the measured RTF. One slot therefore sustains about one request every four
seconds. `steady.js` defaults to that rate, so its "no rejections" threshold means what
it says: the run is at capacity, not above it.

## T-14 — streaming TTFA at capacity (NFR-01)

`steady.js`, 60 s at 1 request / 4 s:

| | measured | budget |
|---|---|---|
| TTFA p95 | **133 ms** | 300 ms |
| TTFA p99 | 143 ms | 600 ms |
| failures | 0 | 0 |
| rejections | 0 | 0 |

## T-13 — overload rejection latency (NFR-05, US-12)

`spike.js`, 40 s at 6 rps, roughly 24× the sustainable rate:

| | measured | budget |
|---|---|---|
| time to a 503, p95 | **16 ms** | 50 ms |
| time to a 503, median | 5 ms | — |
| served streams, TTFA p95 | 27 ms | 300 ms |
| failures (neither served nor cleanly refused) | **0** | 0 |

195 of 240 requests were refused, which is what 24× capacity should produce. The point is
that they were refused *fast* and that streams kept being served while sync work queued.

## Cache profile (ADR-006)

`cache.js`, 60 s at 8 rps with 90% repeated phrases:

| | measured |
|---|---|
| cache hit ratio | **95.3%** |
| hit latency p95 | 46 ms |
| miss latency p95 | 9.6 s |
| failures | 0 |

Hit and miss are reported separately on purpose: one reads an object, the other runs a
model, and a combined percentile measures neither.

## What this run changed

The first spike run reported a p95 rejection latency of **2 s**, not 16 ms — requests
waited out the full stream budget before being told no. Two gaps caused it:

1. **ADR-007 specifies a bounded queue per class; there was no bound.** Any number of
   callers could queue and wait the budget.
2. **A bound alone was not enough.** Callers admitted into a short queue still waited 2 s
   for the same refusal. The dispatcher now also refuses when the queue ahead of a caller
   cannot drain inside its class budget — `depth × mean service time` against the budget
   — which is the literal reading of "never accept-then-timeout" (NFR-05).

With both in place, p95 rejection latency went from 2 s to 16 ms and no request ended up
in a state that was neither served nor cleanly refused.
