# ADR-001: Gateway implemented in Go

Status: Accepted

## Context
The gateway is I/O-bound (HTTP, WebSocket, gRPC streaming, Redis, Postgres) and must
co-exist on cheap nodes where RAM is reserved for ONNX workers (~4 GB each). Streaming
needs first-class cancellation propagation (NFR-01, NFR-05). The team's deepest
expertise is Java/Spring; Go is a secondary skill we want to demonstrate.

## Decision
We will implement the gateway in Go 1.23 with `chi` for HTTP, `grpc-go` for worker
calls, `sqlc` for Postgres, `go-redis`, and `oapi-codegen` for the public API.

## Alternatives considered
| Option | Pros | Cons |
|--------|------|------|
| Spring Boot 3 (WebFlux/virtual threads) | Team expertise, mature ecosystem, gRPC support | 300–500 MB RSS, slower cold start, reactive streaming more ceremony |
| Node.js/NestJS | Fast to write, good streaming | Single-threaded CPU for encoding, weaker typing for backpressure logic |
| Rust (axum/tonic) | Smallest footprint | Slower iteration, less familiar |

## Consequences
Small binaries, ~40 MB RSS, `context.Context` gives cancellation for free. We lose
Spring's batteries (Actuator, Security); we replace them with OTel, Prometheus and
hand-written middleware, which is explicit and reviewable. If a Java showcase is later
desired, the gateway contract (`openapi.yaml`, `worker.proto`) allows a parallel
implementation.
