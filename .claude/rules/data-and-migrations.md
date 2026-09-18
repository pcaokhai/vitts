---
paths: ["gateway/migrations/**", "gateway/internal/storage/**"]
---
# Data and migration rules

- Migrations are `goose` SQL, forward-only, numbered, one concern per file, with a `-- +goose Down`
  that is safe or explicitly `SELECT 'irreversible'`.
- Expand/contract: add column → deploy code that writes both → backfill → switch reads →
  drop old column in a later release. Never rename in place.
- New tenant-owned tables must have `tenant_id uuid not null` and an index starting with it.
- `synth_requests` is range-partitioned by month; the partition job must exist before any
  write to a new month (see `06-flows.md` FL-07).
- Never store request text in Postgres except the encrypted opt-in column (ADR-008).
- Redis keys follow `05-data-model.md`; new key patterns are documented there first, with TTL.
