# Manual test guide

Every feature in the repo, in the order a person can actually check them, with the exact
command and what a pass looks like. Written to be run top to bottom on a clean machine.

Automated coverage lives in `10-tests.md`; this file is for the things a person verifies
with their own eyes and ears — audio that sounds right, a console that behaves, a refusal
that arrives fast.

## 0. Before you start

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

## 1. Health and readiness

| Check | Command | Pass |
|-------|---------|------|
| Liveness | `curl -s $GATEWAY/healthz` | `200`, no dependency touched |
| Readiness | `curl -s $GATEWAY/readyz \| python3 -m json.tool` | `"ready": true`, all of postgres/redis/workers `ok` |
| Contract | `curl -s $GATEWAY/openapi.json \| head -c 80` | The OpenAPI this build implements, served from the binary |

Now prove readiness means something. Stop the worker and watch:

```bash
docker stop vitts-worker-1
sleep 8
curl -s $GATEWAY/readyz | python3 -m json.tool
curl -s $GATEWAY/healthz -o /dev/null -w '%{http_code}\n'
docker start vitts-worker-1
```

**Pass:** `/readyz` is 503 and names `workers` as failing, while `/healthz` stays 200. A
container that is alive but not ready must not be restarted by its orchestrator.

---

## 2. Voices

```bash
curl -s $GATEWAY/v1/voices | python3 -m json.tool | head -20
curl -s $GATEWAY/v1/voices -H "Authorization: Bearer $KEY" | python3 -c 'import json,sys; print(len(json.load(sys.stdin)), "voices")'
```

**Pass:** eight voices **both with and without a key**. US-13 makes the catalogue public;
a key only adds that tenant's own voices.

---

## 3. Synchronous synthesis

```bash
curl -s -X POST $GATEWAY/v1/synthesize \
  -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"text":"Xin chào, đây là bản kiểm tra tổng hợp giọng nói tiếng Việt.","voice":"maichi","format":"wav"}' \
  -o /tmp/sync.wav -D /tmp/sync.headers
grep -iE 'content-type|x-ratelimit' /tmp/sync.headers
afplay /tmp/sync.wav   # macOS; use `aplay` on Linux
```

**Pass:** a WAV you can hear, correct Vietnamese, and `X-RateLimit-Remaining` counting
down. Listen for the things the model is meant to handle:

```bash
for t in "Hôm nay là ngày 20 tháng 9 năm 2026." \
         "Số dư của bạn là 1.250.000 đồng." \
         "Đơn hàng ABC-123 đã được giao." ; do
  curl -s -X POST $GATEWAY/v1/synthesize -H "Authorization: Bearer $KEY" \
    -H 'Content-Type: application/json' -d "{\"text\":\"$t\",\"voice\":\"maichi\"}" -o /tmp/n.wav
  afplay /tmp/n.wav
done
```

**Pass:** dates, money and alphanumeric codes are read as Vietnamese words, not spelled
out character by character (F-05).

### Formats and sample rates

```bash
for f in wav mp3 ogg_opus; do
  curl -s -X POST $GATEWAY/v1/synthesize -H "Authorization: Bearer $KEY" \
    -H 'Content-Type: application/json' \
    -d "{\"text\":\"Kiểm tra định dạng.\",\"voice\":\"maichi\",\"format\":\"$f\"}" \
    -o /tmp/out.$f -w "$f: %{size_download} bytes\n"
done
```

**Pass:** all three play, and mp3/ogg are clearly smaller than wav.

---

## 4. Streaming

```bash
time curl -N -s -X POST $GATEWAY/v1/synthesize/stream \
  -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"text":"Đây là một câu dài hơn để nghe rõ độ trễ của luồng phát trực tuyến.","voice":"maichi"}' \
  -o /tmp/stream.wav
afplay /tmp/stream.wav
```

**Pass:** audio starts arriving well before the request finishes, and the file plays as
one continuous, correctly ordered utterance.

### Cancellation

```bash
timeout 1 curl -N -s -X POST $GATEWAY/v1/synthesize/stream \
  -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"text":"Một câu rất dài để có thời gian huỷ giữa chừng, nhiều từ, nhiều âm tiết, nhiều đoạn.","voice":"maichi"}' \
  -o /tmp/partial.wav
docker logs vitts-gateway-1 --since 30s 2>&1 | grep -i cancel | tail -3
curl -s "$GATEWAY/v1/usage?from=$(date -u +%Y-%m-%d)&to=$(date -u +%Y-%m-%d)" \
  -H "Authorization: Bearer $KEY" | python3 -m json.tool
```

**Pass:** the worker slot is released within ~200 ms of the hang-up, and usage grows by
roughly the audio delivered rather than the whole text (ADR-010).

### WebSocket

```bash
npx --yes wscat -c "$GATEWAY/v1/synthesize/ws" -H "Authorization: Bearer $KEY"
# then paste: {"text":"Xin chào từ WebSocket.","voice":"maichi"}
```

**Pass:** binary frames come back in order and the socket stays open for the next
utterance. This is the path a voice bot uses for barge-in.

---

## 5. Limits and refusals

Each of these should be **fast** — NFR-05 forbids accept-then-timeout.

### Rate limit (429)

```bash
for i in $(seq 1 40); do
  curl -s -o /dev/null -w '%{http_code} ' -X POST $GATEWAY/v1/synthesize \
    -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
    -d '{"text":"nhanh","voice":"maichi"}'
done; echo
```

**Pass:** 200s then 429s. Check the refusal carries its hint:

```bash
curl -s -D- -o /dev/null -X POST $GATEWAY/v1/synthesize \
  -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"text":"nhanh","voice":"maichi"}' | grep -iE 'HTTP/|retry-after|x-ratelimit'
```

### Input bounds

| Case | Command | Pass |
|------|---------|------|
| Too long | `-d "{\"text\":\"$(python3 -c 'print("a"*4000)')\"}"` | `413 text_too_long` |
| Unknown voice | `-d '{"text":"x","voice":"khong-co"}'` | `422 unknown_voice` |
| Empty text | `-d '{"text":""}'` | `400 invalid_request` |
| No key | omit the header | `401 unauthorized` |
| Wrong key | `Bearer zt_live_nope` | `401 unauthorized` |

All five must answer `application/problem+json` with a stable `code`:

```bash
curl -s -X POST $GATEWAY/v1/synthesize -H 'Content-Type: application/json' \
  -d '{"text":"x"}' | python3 -m json.tool
```

### Overload (503)

```bash
docker stop vitts-worker-1
curl -s -D- -o /dev/null -X POST $GATEWAY/v1/synthesize \
  -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"text":"không có worker","voice":"maichi"}' | grep -iE 'HTTP/|retry-after'
docker start vitts-worker-1
```

**Pass:** `503` with `code: overloaded` and a `Retry-After`, returned in milliseconds —
not a 500, and not a hang.

---

## 6. Cache

Send the same request twice and compare:

```bash
for i in 1 2; do
  curl -s -o /dev/null -w "attempt $i: %{time_total}s\n" -X POST $GATEWAY/v1/synthesize \
    -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
    -d '{"text":"Câu này sẽ được lưu vào bộ nhớ đệm.","voice":"maichi","format":"mp3"}'
done
```

**Pass:** the second is dramatically faster. Then prove the key is deterministic but
strict — whitespace and `0.8` vs `0.80` hit the same entry, a different voice does not:

```bash
curl -s $GATEWAY/metrics | grep '^cache_total'
```

**Pass:** `cache_total{result="hit"}` increased. A hit still bills the tenant (ADR-006) —
check usage grew for both calls.

---

## 7. Quota

`pro` allows 20M characters, so to see a refusal use a `free` tenant (100k):

```bash
FREEKEY=$(curl -fsS -X POST $GATEWAY/admin/v1/tenants -H "X-Admin-Key: $ADMIN" \
  -H 'Content-Type: application/json' -d '{"name":"Quota test","plan_id":"free"}' \
  | python3 -c 'import json,sys; print(json.load(sys.stdin)["key"]["secret"])')
```

Burning 100k characters through the API takes a while; the honest quick check is that the
counter moves and the report agrees with it. Spend a little, then:

```bash
curl -s "$GATEWAY/v1/usage?from=$(date -u -v-7d +%Y-%m-%d)&to=$(date -u +%Y-%m-%d)" \
  -H "Authorization: Bearer $FREEKEY" | python3 -m json.tool
```

**Pass:** `totals.chars` matches what you sent, and `plan_chars_per_month` is 100000.
A tenant that does exceed it gets `402 quota_exceeded`, not 429 (US-07).

---

## 8. Jobs

```bash
TEXT=$(python3 -c 'print("Đây là một đoạn văn bản dài để kiểm tra công việc nền. " * 40)')
JOB=$(curl -fsS -X POST $GATEWAY/v1/jobs \
  -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -H "Idempotency-Key: manual-$(date +%s)" \
  -d "{\"text\":\"$TEXT\",\"voice\":\"maichi\",\"format\":\"mp3\"}" \
  | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')
echo "job $JOB"

while :; do
  curl -s $GATEWAY/v1/jobs/$JOB -H "Authorization: Bearer $KEY" \
    | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d["status"], d.get("segments_done"), "/", d.get("segments_total"))'
  sleep 2
done
```

**Pass:** `queued → segmenting → synthesizing → merging → completed`, with
`segments_done` climbing. Then fetch the result:

```bash
URL=$(curl -s $GATEWAY/v1/jobs/$JOB -H "Authorization: Bearer $KEY" \
  | python3 -c 'import json,sys; print(json.load(sys.stdin)["output_url"])')
curl -s "$URL" -o /tmp/job.mp3 && afplay /tmp/job.mp3
```

**Pass:** the signed URL works **from your machine** (not a `minio:9000` address only the
container network can reach), and the merged audio plays as one piece with no gaps or
repeated segments at the joins.

### Idempotency

```bash
IK="manual-idem-$(date +%s)"
for i in 1 2; do
  curl -s -o /tmp/j$i.json -w "%{http_code} " -X POST $GATEWAY/v1/jobs \
    -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' -H "Idempotency-Key: $IK" \
    -d '{"text":"Kiểm tra idempotency.","voice":"maichi"}'
done; echo
python3 -c 'import json; a=json.load(open("/tmp/j1.json")); b=json.load(open("/tmp/j2.json")); print("same job:", a["id"]==b["id"])'
```

**Pass:** both 202, same job id. Now change the body with the same key:

```bash
curl -s -o /dev/null -w '%{http_code}\n' -X POST $GATEWAY/v1/jobs \
  -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' -H "Idempotency-Key: $IK" \
  -d '{"text":"Nội dung khác hẳn.","voice":"maichi"}'
```

**Pass:** `409 idempotency_conflict`. A missing header is `400`.

### Cancel

```bash
JOB2=$(curl -fsS -X POST $GATEWAY/v1/jobs -H "Authorization: Bearer $KEY" \
  -H 'Content-Type: application/json' -H "Idempotency-Key: cancel-$(date +%s)" \
  -d "{\"text\":\"$TEXT\",\"voice\":\"maichi\"}" | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')
curl -s -o /dev/null -w '%{http_code}\n' -X DELETE $GATEWAY/v1/jobs/$JOB2 -H "Authorization: Bearer $KEY"
curl -s $GATEWAY/v1/jobs/$JOB2 -H "Authorization: Bearer $KEY" | python3 -m json.tool | grep status
```

**Pass:** `cancelled`, and no further segments are dispatched.

---

## 9. Tenant isolation

The single most important check in the repo. Make a second tenant and try to read the
first one's job:

```bash
OTHER=$(curl -fsS -X POST $GATEWAY/admin/v1/tenants -H "X-Admin-Key: $ADMIN" \
  -H 'Content-Type: application/json' -d '{"name":"Other tenant","plan_id":"free"}' \
  | python3 -c 'import json,sys; print(json.load(sys.stdin)["key"]["secret"])')

curl -s -o /dev/null -w 'other tenant reading my job: %{http_code}\n' \
  $GATEWAY/v1/jobs/$JOB -H "Authorization: Bearer $OTHER"
curl -s $GATEWAY/v1/jobs -H "Authorization: Bearer $OTHER" | python3 -c 'import json,sys; print(len(json.load(sys.stdin)), "jobs visible")'
```

**Pass:** `404`, never `403` — a 403 would confirm the resource exists. And the other
tenant sees zero of your jobs.

---

## 10. Key management

```bash
NEW=$(curl -fsS -X POST $GATEWAY/v1/keys -H "Authorization: Bearer $KEY" \
  -H 'Content-Type: application/json' -d '{"name":"Read only","scopes":["usage"]}')
echo "$NEW" | python3 -m json.tool
NEWKEY=$(echo "$NEW" | python3 -c 'import json,sys; print(json.load(sys.stdin)["secret"])')
```

**Pass:** the secret appears exactly once, here. Now check the scope actually binds:

```bash
curl -s -o /dev/null -w 'usage with usage scope: %{http_code}\n' \
  "$GATEWAY/v1/usage?from=$(date -u +%Y-%m-%d)&to=$(date -u +%Y-%m-%d)" -H "Authorization: Bearer $NEWKEY"
curl -s -o /dev/null -w 'synthesize with usage scope: %{http_code}\n' -X POST $GATEWAY/v1/synthesize \
  -H "Authorization: Bearer $NEWKEY" -H 'Content-Type: application/json' -d '{"text":"x"}'
```

**Pass:** `200` then `403 forbidden_scope`.

### Revoke takes effect immediately

```bash
ID=$(echo "$NEW" | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')
curl -s -o /dev/null -w 'revoke: %{http_code}\n' -X DELETE $GATEWAY/v1/keys/$ID -H "Authorization: Bearer $KEY"
curl -s -o /dev/null -w 'revoked key: %{http_code}\n' \
  "$GATEWAY/v1/usage?from=$(date -u +%Y-%m-%d)&to=$(date -u +%Y-%m-%d)" -H "Authorization: Bearer $NEWKEY"
```

**Pass:** `204` then `401` with no cache delay. The revoked key also disappears from
`GET /v1/keys` — the API lists live keys only.

---

## 11. Usage report

```bash
curl -s "$GATEWAY/v1/usage?from=$(date -u -v-7d +%Y-%m-%d)&to=$(date -u +%Y-%m-%d)" \
  -H "Authorization: Bearer $KEY" | python3 -m json.tool
curl -s "$GATEWAY/v1/usage?from=$(date -u -v-7d +%Y-%m-%d)&to=$(date -u +%Y-%m-%d)&format=csv" \
  -H "Authorization: Bearer $KEY"
```

**Pass:** JSON totals match the sum of the days; CSV has a header row and RFC 4180
quoting. Note the report reads the daily rollup, so it lags live traffic by up to the
rollup interval — that is US-16 AC-2, not a bug.

---

## 12. Admin surface

```bash
curl -s -o /dev/null -w 'no admin key: %{http_code}\n' $GATEWAY/admin/v1/tenants -X POST \
  -H 'Content-Type: application/json' -d '{"name":"x","plan_id":"free"}'
WRONG="${ADMIN}x"   # right length, wrong value
curl -s -o /dev/null -w 'wrong admin key: %{http_code}\n' $GATEWAY/admin/v1/tenants -X POST \
  -H "X-Admin-Key: $WRONG" -H 'Content-Type: application/json' \
  -d '{"name":"x","plan_id":"free"}'
curl -s -o /dev/null -w 'tenant key on admin: %{http_code}\n' $GATEWAY/admin/v1/tenants -X POST \
  -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' -d '{"name":"x","plan_id":"free"}'
```

**Pass:** `401`/`403` on all three. A tenant key never opens the admin surface, and the
allowlist refuses anything outside the operator network.

---

## 13. Console

Open `http://127.0.0.1:3001`.

| Step | Pass |
|------|------|
| Paste a bad key | A message about the key, from the API, not a generic error |
| Paste `$KEY` | Lands on Usage |
| Usage chart | One bar per day across the range; quiet days are faint marks, not gaps |
| Range 7/30/90 | Figures and chart change together |
| `usage.csv` | Downloads a real CSV, not a 401 document |
| Keys → create | Secret shown once, in the red panel; list refreshes |
| Keys → revoke | Row disappears; the count drops |
| Forget | Returns to the gate |
| Close the tab, reopen | Gate again — the key lives in `sessionStorage` only |
| `/docs` | Readable **without** a key; "Sign in" in the corner |
| Phone width | Table collapses to stacked rows, no horizontal scroll |

Keep the browser console open throughout. **Pass:** no errors at any step.

---

## 14. SDKs

```bash
cd sdk/python && uv sync && cd ../..
cd sdk/js && npm ci && cd ../..
```

Python:

```bash
cd sdk/python && VITTS_KEY="$KEY" GATEWAY="$GATEWAY" uv run python - <<'PY'
import os
from vitts import JobCreateRequest, SynthesizeRequest, VittsClient, job_status

with VittsClient(os.environ["VITTS_KEY"], os.environ["GATEWAY"]) as c:
    print("voices:", len(c.list_voices()))
    print("sync:  ", len(c.synthesize(SynthesizeRequest(text="Xin chào.")).data), "bytes")
    print("stream:", sum(len(x) for x in c.stream(SynthesizeRequest(text="Xin chào."))), "bytes")
    job = c.create_job(JobCreateRequest(text="Một đoạn dài hơn cho công việc nền."))
    print("job:   ", job_status(c.wait_for_job(str(job.id), poll=2, timeout=300)))
PY
cd ../..
```

JavaScript:

```bash
cd sdk/js && VITTS_KEY="$KEY" GATEWAY="$GATEWAY" node --experimental-strip-types - <<'TS'
import { VittsClient } from "./src/index.ts";
const c = new VittsClient({ apiKey: process.env.VITTS_KEY!, baseUrl: process.env.GATEWAY! });
console.log("voices:", (await c.listVoices()).length);
console.log("sync:  ", (await c.synthesize({ text: "Xin chào." })).bytes.length, "bytes");
let n = 0; for await (const ch of c.stream({ text: "Xin chào." })) n += ch.length;
console.log("stream:", n, "bytes");
const job = await c.createJob({ text: "Một đoạn dài hơn cho công việc nền." });
console.log("job:   ", (await c.waitForJob(job.id!, { pollMs: 2000 })).status);
TS
cd ../..
```

**Pass:** both print voices, byte counts and `completed`.

---

## 15. Observability

| Where | Check |
|-------|-------|
| `curl -s $GATEWAY/metrics \| grep -c '^vitts\|^http_\|^tts_'` | Metrics are being emitted |
| `$GATEWAY/metrics` | `tts_ttfa_seconds`, `dispatch_queue_depth`, `cache_total`, `usage_chars_total`, `worker_slots_busy` all present |
| Prometheus `:9090` → Status → Targets | `gateway` is UP |
| Prometheus → Alerts | 10 rules loaded, none firing on a healthy stack |
| Grafana `:3000` (admin/admin) | The ViTTS overview dashboard has data in every panel |

Then make an alert *nearly* fire, so you know the wiring is real: stop the worker and
watch `worker_ready` drop to 0 and `NoReadyWorker` go pending in Prometheus.

---

## 16. Resilience

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

## 17. The automated suites, by hand

```bash
make lint              # Go, Python, TypeScript, CSS, gitleaks, buf
make test              # unit: Go, Python, console, both SDKs
make test-integration  # testcontainers: Postgres, Redis, MinIO, fake worker
make generate && git diff --exit-code   # contract drift, including SDK types
make audit             # govulncheck, pip-audit, npm audit
make smoke             # health, sync, stream, job end to end
make bench             # worker RTF and TTFA on this machine
```

**Pass:** all green, and `make generate` leaves the tree clean.

Load tests need a key and a scenario:

```bash
VITTS_KEY="$KEY" VITTS_SCENARIO=steady make loadtest
```

**Pass:** the thresholds in `scripts/loadtest/` hold; a 429 or 503 is a *measurement*, not
a failure, and its latency should be in the milliseconds.

---

## 18. Known gaps

Things this guide cannot check, and why:

| Gap | Why |
|-----|-----|
| Webhook delivery | Every local receiver is on a private address and the SSRF guard correctly refuses it. Needs a public HTTPS endpoint, e.g. a tunnel. |
| Quota exhaustion | Burning a plan's full allowance takes real traffic; the counter and the report are checked instead. |
| Model upgrade | Needs a second pinned `VITTS_MODEL_REVISION` to mean anything. |
| Real pricing | `config/plans.yaml` is still `TODO(pricing)` placeholders. |

## Tear down

```bash
make down    # also drops volumes: model weights re-download next time
```

To keep the weights, stop without `-v`:

```bash
docker compose --env-file .env.example -f deploy/docker-compose.yml down
```
