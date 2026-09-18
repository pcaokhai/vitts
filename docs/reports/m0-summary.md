# M0 close-out — spike and foundations

Covers tasks 0.1–0.6 of `docs/12-implementation-plan.md`. Per-task plans are in
`docs/plans/0.*.md`; this page records what changed outside those plans, what was decided,
and what was verified. Benchmark numbers live in `bench-m0.md`.

## What shipped

| Task | Output | Commit |
|------|--------|--------|
| 0.1 | `Makefile`, `.editorconfig`, `.gitignore`, `CODEOWNERS`, lint-only CI | `a1f06f4` |
| 0.2 | `proto/worker.proto` at its ADR-009 location, `buf` codegen for Go and Python, `gateway/go.mod` | `c7ab53f` |
| 0.3 | Worker engine, validated config, `Health` RPC, warm-up, S3 model mirror | `4847aaf` |
| 0.4 | `Synthesize` streaming with cooperative cancellation, PCM16 + resampling | `dc95e82` |
| 0.5 | `scripts/bench.py`, `bench-m0.md`, PRD assumptions A1/A2 answered | `c11544f` |
| 0.6 | Compose stack, worker image, `make smoke` | `c11544f` |
| — | CI fixes after the first push, ADR-010, chunker retraction | `d1c1498` |

The Makefile declares every command in `CLAUDE.md`; a target whose owning task has not
landed prints what it is waiting on and exits 0, so CI is green at every point in the
sequence rather than only at the end.

## Decisions taken during M0

| # | Decision | Where |
|---|----------|-------|
| 1 | Go 1.23 → 1.25: current `grpc-go` and `protobuf` releases no longer build under 1.23 | `docs/14-engineering-standards.md`, `docs/02-architecture.md` |
| 2 | Three `buf lint` exceptions with written reasons, instead of renaming a contract that has no callers | `proto/buf.yaml` |
| 3 | Weights are materialised with `snapshot_download(local_dir=…)`, never the shared HF blob cache | `docs/plans/0.3.md` |
| 4 | `VITTS_MODEL_REVISION` has no default; boot fails without it | `worker/vitts_worker/config.py` |
| 5 | A busy worker returns `RESOURCE_EXHAUSTED` immediately; queueing is the gateway's job | ADR-007, `docs/plans/0.4.md` |
| 6 | A cancelled stream is metered from delivered audio at a published 14.5 chars/s, capped at the character count | **ADR-010** |
| 7 | Container readiness is the `Health` RPC, not an open port | `docs/plans/0.6.md` |
| 8 | `.env.example` is the compose env file, so the pinned revision exists in one place | `Makefile` |

Only #6 needed a new ADR. #1 and #3 changed existing docs in the same PR that changed the
code, per `CLAUDE.md`.

## Bugs found and fixed at the root

| Symptom | Root cause | Fix |
|---------|-----------|-----|
| onnxruntime refused the codec graph: *"External data path escapes model directory"* | HF cache snapshots are symlinks into `blobs/`; ORT rejects external data resolving outside the model dir | materialise real files via `local_dir`, which also makes the directory mirrorable to S3 verbatim |
| Worker container crashed on startup, `Failed to bind to address :50051` | `VITTS_GRPC_ADDR` defaulted to Go's `:port`, which `grpc.aio` cannot bind | normalise in `config.py` so every caller is fixed, not just compose; covered by a test |
| CI `contract-drift` failed on the runner but passed locally | `scripts/postgen.sh` used `sed -i ''`, the BSD/macOS form; GNU sed reads `''` as the script | replaced with `scripts/postgen.py` |
| CI `lint` failed inside `gitleaks-action` with no finding | the action scans `<first-commit>^..HEAD`, and the first commit of a repository has no parent | run the `gitleaks` binary from `make lint`, pinned by `scripts/setup.sh`; identical locally and in CI |

## Corrections to earlier claims

- **Retracted:** the task 0.3 note that `zerotts` exports no chunker. It does —
  `zerotts.chunking.chunk_text(text, max_chunk_sec=15.0)`, exactly the signature US-03
  names, lossless on non-whitespace characters. It is not re-exported from the package
  `__init__`, which is why a top-level scan missed it. `docs/02-architecture.md` was
  right and `Segment` stays a thin wrapper (task 2.2).
- **A1 is not closed.** It is stated for Hetzner CCX / AWS c7i; M0 measured Apple
  silicon. `docs/01-PRD.md` says "partly confirmed" and names what is still required.

## Verification

Run on the target machine, MacBook M4 Pro (12 cores), and in GitHub Actions.

| Command | Result |
|---------|--------|
| `make lint` | gitleaks, buf, golangci-lint, ruff, `mypy --strict` — clean |
| `make test` | 39 unit tests |
| `make test-model` | 7 tests against the pinned weights |
| `make generate` | no drift (T-17) |
| `make up` | postgres, redis, minio, worker all healthy |
| `make smoke` | Health ready; 7 frames, 3.12 s audio, TTFA 88 ms, RTF 0.55× in-container |
| CI | `lint`, `test`, `contract-drift` all green on `main` |

Tests added in M0: T-01, T-02, T-21, T-22, T-23, T-24, T-25, T-26. T-17 closed.

## Deliberately not done

- `Segment` and `Merge` return `UNIMPLEMENTED` naming task 2.2; `Synthesize` params are
  wired but mp3/ogg containers are not.
- The smoke check covers the worker leg only; gateway legs land with 1.1, 1.9, 1.10, 2.3.
- Image hardening and digest pinning for the worker image are task 3.3. MinIO is already
  pinned by digest because its Docker Hub tags did not resolve.
- `T-23`/`T-25` are excluded from CI: they download ~200 MB of weights. They need a
  nightly job.
