# ADR-004: gRPC server-streaming between gateway and worker

Status: Accepted

## Context
Audio chunks must flow to the client within ~70 ms of generation and stop immediately
when the client disconnects (NFR-01, cost). Internal calls need deadlines, health checks
and typed contracts.

## Decision
We will define `worker.proto` with `Synthesize` (server-streaming PCM16 frames),
`Segment` (unary) and `Health` (unary), and propagate gateway request context as gRPC
deadline and cancellation.

## Alternatives considered
| Option | Pros | Cons |
|--------|------|------|
| HTTP/1.1 chunked | Simple, curl-able | Weak cancel semantics, no typed contract, no flow control |
| ZeroMQ / raw sockets | Fast | Hand-rolled framing, health, retries |
| Message queue for stream | Durable | Adds latency; streams are ephemeral by nature |

## Consequences
HTTP/2 flow control gives natural backpressure per stream. Proto is the single contract;
both sides generate code in CI and fail on drift.
