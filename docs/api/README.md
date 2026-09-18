# API contracts

- `openapi.yaml` — the public REST contract. Hand-edited; generated server stubs land in
  `gateway/api/` (task 1.14).
- The gateway↔worker gRPC contract is **`/proto/worker.proto`** — moved there in task 0.2
  per ADR-009 and `docs/02-architecture.md` § 2. Generated stubs live in
  `gateway/internal/gen/workerpb/` and `worker/vitts_worker/gen/` and are never hand-edited.

Change the contract first, then run `make generate`; CI fails on drift (T-17).
