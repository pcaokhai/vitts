# Permissions

## Principals

| Principal | Credential | Where scope is derived |
|-----------|------------|------------------------|
| Tenant API key | `Authorization: Bearer zt_live_…` | `api_keys.scopes` (DB, cached 60 s) |
| Admin | `X-Admin-Key` header | Environment variable; single operator; IP allowlist |
| Internal (scheduled jobs, orchestrator) | In-process | No HTTP surface |
| Worker | gRPC on private network | Network isolation (mTLS in `deploy/` when cross-host) |

## Scopes

| Scope | Grants |
|-------|--------|
| `synth` | `/v1/synthesize`, `/v1/synthesize/stream`, `/v1/synthesize/ws`, `/v1/voices` |
| `jobs` | `/v1/jobs*` |
| `usage` | `/v1/usage` |
| `keys` | `/v1/keys*` (create/list/revoke keys of own tenant) |

Default key on tenant creation: `[synth, jobs, usage, keys]`. Keys created later default
to `[synth]`.

## Resource × operation × principal

| Resource | Operation | Tenant key | Admin | Notes |
|----------|-----------|-----------|-------|-------|
| Synthesis | create | `synth` | — | Tenant quota/limits apply |
| Job | create/read/list/cancel | `jobs`, own tenant only | read any | 404 for other tenants' jobs (not 403) |
| Job output | download | signed URL holder | — | URL expiry 24 h |
| Voice | list | any key or anonymous | manage | Tenant-scoped voices visible to owner only |
| Usage | read | `usage`, own tenant | read any | |
| API key | create/list/revoke | `keys`, own tenant | manage any | Secret returned once |
| Tenant | create/suspend/plan change | — | yes | Audit logged |
| Plan | manage | — | yes (migration/seed) | |

## Enforcement

- Every repository method takes `tenantID` as a parameter; there is no method that reads
  tenant-owned rows without it (enforced by review and by the `sqlc` query set).
- No row-level security in Postgres for MVP; isolation is code-enforced and tested by
  T-12 (`10-tests.md`).
- Object storage is never exposed directly; only signed URLs scoped to one object.
- Admin endpoints live under `/admin/` and are excluded from the public OpenAPI served
  to tenants.
