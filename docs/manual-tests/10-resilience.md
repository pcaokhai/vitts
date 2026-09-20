# 10. Resilience drills

The runbook drills. Each one found a real bug the first time it ran.

## Resilience

These are the drills from `13-runbook.md`. Each one found a real bug the first time it was
run (`docs/reports/drill-m3.md`), so they are worth repeating after any change to the
dispatcher, the limiter or the queue.

### Redis restart

```bash
docker restart vitts-redis-1
sleep 2
curl -s -o /dev/null -w 'during restart: %{http_code}\n' -X POST $GATEWAY/v1/synthesize \
  -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' -d '{"text":"Redis restart","voice":"maichi"}'
sleep 5
curl -s -o /dev/null -w 'after restart:  %{http_code}\n' -X POST $GATEWAY/v1/synthesize \
  -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' -d '{"text":"Sau khi Redis quay lại","voice":"maichi"}'
curl -s $GATEWAY/metrics | grep '^dependency_degraded_total'
```

**Pass:** requests keep succeeding throughout, `dependency_degraded_total` records the
window in which limits were not enforced, and the gateway recovers **without a restart**
(the Lua script cache is repopulated automatically).

### Worker replacement

```bash
docker stop vitts-worker-1 && sleep 6
curl -s $GATEWAY/readyz | python3 -m json.tool
docker start vitts-worker-1
until curl -sf $GATEWAY/readyz >/dev/null; do sleep 2; done; echo "ready again"
```

**Pass:** `/readyz` goes 503 then 200 with no gateway restart, and the weights volume is
reused so recovery is seconds rather than minutes.

### Postgres restore (T-20)

```bash
docker exec vitts-postgres-1 pg_dump -U vitts -d vitts -Fc -f /tmp/nightly.dump
docker exec vitts-postgres-1 psql -U vitts -d postgres -q \
  -c "drop database if exists vitts_restore;" -c "create database vitts_restore owner vitts;"
docker exec vitts-postgres-1 pg_restore -U vitts -d vitts_restore --no-owner /tmp/nightly.dump
docker exec vitts-postgres-1 psql -U vitts -d vitts_restore -tAc \
  "select 'tenants='||count(*) from tenants; select 'partitions='||count(*) from pg_inherits i join pg_class c on c.oid=i.inhparent where c.relname='synth_requests';"
```

**Pass:** row counts match the original, the `synth_requests` partitions and
`ensure_synth_requests_partition()` come back with the dump, and re-running migrations
against the restore is a no-op.

### Schema guard

```bash
docker exec vitts-postgres-1 psql -U vitts -d vitts_restore -q -c "delete from goose_db_version where version_id=2;"
docker run --rm --network vitts_default \
  --env-file <(grep -E '^VITTS_(ADMIN_KEY|WEBHOOK_SIGNING_SECRET|ADMIN_IP_ALLOWLIST)=' .env.example) \
  -e VITTS_DATABASE_URL="postgres://vitts:vitts@postgres:5432/vitts_restore?sslmode=disable" \
  -e VITTS_REDIS_URL=redis://redis:6379/0 -e VITTS_WORKER_ADDRS=worker:50051 \
  -e VITTS_S3_ENDPOINT=http://minio:9000 -e VITTS_S3_BUCKET=vitts \
  -e VITTS_S3_ACCESS_KEY=vitts-dev -e VITTS_S3_SECRET_KEY=vitts-dev-secret \
  vitts-gateway
```

**Pass:** the gateway refuses to start, naming both versions. A binary must never serve a
schema older than its own migrations.

---

---

[← Observability](09-observability.md) · [Index](README.md) · [Automated suites →](11-automated-suites.md)
