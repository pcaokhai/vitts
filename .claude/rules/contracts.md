---
paths: ["proto/**", "docs/api/**", "gateway/api/**", "worker/vitts_worker/gen/**"]
---
# Contract rules

- `docs/api/openapi.yaml` and `proto/worker.proto` are the only hand-edited contract files.
  Generated code is committed but never hand-edited.
- Run `make generate` after any contract change; CI runs `git diff --exit-code`.
- Backward compatibility: never remove or renumber proto fields; add new fields, deprecate
  old ones. REST breaking changes require `/v2` and an ADR.
- Every error the API can return is in the `Problem.code` enum. Add there first.
- `buf lint` and `buf breaking --against .git#branch=main` must pass.
