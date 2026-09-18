# Variables and secrets

All configuration is via environment variables, loaded once at boot and validated; the
process exits non-zero with a clear message on any invalid or missing required value.
No secret is ever bundled into a client, image layer, or log line.

## Gateway

| Name | Used by | Scope | Source | Rotation | Risk |
|------|---------|-------|--------|----------|------|
| `VITTS_ENV` | all | server | env | — | low |
| `VITTS_HTTP_ADDR` | http | server | env | — | low |
| `VITTS_DATABASE_URL` | storage | server | secret store | quarterly | high (data) |
| `VITTS_REDIS_URL` | storage | server | secret store | quarterly | medium |
| `VITTS_S3_ENDPOINT` / `_BUCKET` / `_REGION` | storage | server | env | — | low |
| `VITTS_S3_ACCESS_KEY` / `_SECRET_KEY` | storage | server | secret store | quarterly | high |
| `VITTS_WORKER_ADDRS` | dispatch | server | env / DNS | — | low |
| `VITTS_ADMIN_KEY` | admin | server | secret store | on operator change | critical |
| `VITTS_ADMIN_IP_ALLOWLIST` | admin | server | env | — | medium |
| `VITTS_WEBHOOK_SIGNING_SECRET` | jobs | server | secret store | yearly, dual-key overlap | high |
| `VITTS_TEXT_ENCRYPTION_KEY` | usage (opt-in text) | server | secret store | yearly | high (PII) |
| `VITTS_STREAM_QUEUE_TIMEOUT` (2s) / `_SYNC_QUEUE_TIMEOUT` (10s) | dispatch | server | env | — | low |
| `VITTS_STREAM_SLOT_RESERVE_PCT` (30) | dispatch | server | env | — | low |
| `VITTS_LOG_LEVEL` | telemetry | server | env | — | low |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | telemetry | server | env | — | low |

## Worker

| Name | Used by | Scope | Source | Risk |
|------|---------|-------|--------|------|
| `VITTS_GRPC_ADDR` | server | server | env | low |
| `VITTS_MODEL_REPO` (`zeroweight-ai/ZeroTTS`) / `VITTS_MODEL_REVISION` (commit hash) | engine | server | env | medium (pin!) |
| `VITTS_MODEL_MIRROR_S3` | engine | server | env | low |
| `VITTS_MODEL_DIR` (`~/.cache/vitts/model`) | engine | server | env (volume) | low |
| `HF_HOME` | engine | server | env (volume) | low |
| `ORT_INTRA_OP_THREADS` | engine | server | env | low |
| `VITTS_S3_*` | encode/merge | server | secret store | high |
| `VITTS_MAX_TEXT_CHARS` (3000) | server | server | env | low |

## Pre-go-live checklist

- [ ] Secrets injected from the secret store, not `.env` files, in prod compose/k8s.
- [ ] `VITTS_ADMIN_KEY` ≥ 32 random bytes; allowlist set.
- [x] `VITTS_MODEL_REVISION` is a commit hash, not `main` (`c2bfbd6…`, set in `.env.example`).
- [ ] Postgres and Redis not reachable from the internet; TLS on Postgres.
- [ ] S3 bucket private; IAM user limited to the bucket prefix.
- [ ] Logs verified free of `Authorization` headers and request text (grep in staging).
- [ ] Backups: nightly `pg_dump` to S3, restore drill done once.
