# 1. Setup

Ports, the stack, the plan tiers and a tenant key. Nothing else works until this passes.

## Before you start

### Ports

The stack publishes Postgres on 5432, the gateway on 8080 and Grafana on 3000. If another
project already holds those, export alternatives once and use them for the whole session —
every published port is overridable:

```bash
export VITTS_PG_PORT=55432 VITTS_GATEWAY_PORT=18080
export GATEWAY=http://127.0.0.1:$VITTS_GATEWAY_PORT
```

Otherwise:

```bash
export GATEWAY=http://127.0.0.1:8080
```

Everything below uses `$GATEWAY`, so the rest of the guide reads the same either way.

### Bring the stack up

```bash
make up
```

First run downloads ~900 MB of model weights, so the worker takes a few minutes to report
healthy. `make up` always builds: a stale image applies fewer migrations and the gateway
then refuses to start (drill-m3 finding 3).

```bash
docker compose --env-file .env.example -f deploy/docker-compose.yml ps
```

**Pass:** `gateway`, `worker`, `postgres`, `redis`, `minio`, `console` all healthy;
`migrate` and `minio-init` exited 0.

### Seed the plans

Migrations deliberately ship no plan rows, so a production database never inherits
invented pricing (T-30). Load the operator's file:

```bash
VITTS_DATABASE_URL="postgres://vitts:vitts@127.0.0.1:${VITTS_PG_PORT:-5432}/vitts?sslmode=disable" make seed
```

**Pass:** four plans upserted — `free`, `starter`, `pro`, `metered`. Their numbers are
still `TODO(pricing)` placeholders in `config/plans.yaml`.

### Make yourself a tenant

```bash
ADMIN=$(grep '^VITTS_ADMIN_KEY=' .env.example | cut -d= -f2)
KEY=$(curl -fsS -X POST $GATEWAY/admin/v1/tenants \
  -H "X-Admin-Key: $ADMIN" -H 'Content-Type: application/json' \
  -d '{"name":"Manual test","plan_id":"pro"}' \
  | python3 -c 'import json,sys; print(json.load(sys.stdin)["key"]["secret"])')
echo "${KEY:0:12}…"
```

**Pass:** a key starting `zt_live_`. Keep `$KEY` for the rest of the session; it is shown
once and only its hash is stored.

---

---

[Index](README.md) · [Health and voices →](02-health-and-voices.md)
