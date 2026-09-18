# ADR-003: One in-flight inference per worker process

Status: Accepted

## Context
ONNX inference for an autoregressive model saturates its thread pool. Two concurrent
requests in one process contend for the same cores and both miss the TTFA target.
Upstream benchmarks assume a dedicated core pool.

## Decision
Each worker process runs exactly one inference at a time (`asyncio.Lock` /
single-flight). Concurrency comes from running N processes per node, each pinned to
`ORT_INTRA_OP_THREADS` cores. The gateway treats each process as one slot.

## Alternatives considered
| Option | Pros | Cons |
|--------|------|------|
| Multiple concurrent inferences per process | Fewer processes | Unpredictable TTFA, thread thrash |
| Request batching inside the model | Higher throughput | Not supported for streaming; upstream has no batch API |

## Consequences
Capacity is a simple integer (slots). Memory = N × ~4 GB. M0 must benchmark 8×1 vs 4×2
thread/process layouts (assumption A2). Backpressure is a slot count, not a guess.
