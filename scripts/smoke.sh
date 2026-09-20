#!/usr/bin/env bash
# End-to-end check against the local stack: worker Health and streaming, then the gateway
# legs a customer actually uses — sync, stream, job — through a throwaway tenant.
set -euo pipefail
cd "$(dirname "$0")/.."

WORKER_ADDR="${WORKER_ADDR:-127.0.0.1:50051}"
COMPOSE="docker compose --env-file .env.example -f deploy/docker-compose.yml"

fail() { echo "SMOKE FAIL: $*" >&2; exit 1; }
step() { printf '\n== %s\n' "$*"; }

step "stack status"
$COMPOSE ps --status running --format '{{.Service}}\t{{.Status}}' || fail "stack is not up (make up)"

step "worker Health"
( cd worker && uv run python -m vitts_worker.healthcheck "$WORKER_ADDR" ) \
  || fail "worker is not ready at $WORKER_ADDR"

step "worker Synthesize (streaming)"
( cd worker && WORKER_ADDR="$WORKER_ADDR" uv run python ../scripts/smoke_synthesize.py ) \
  || fail "streaming synthesis did not produce audio"

step "gateway liveness and readiness"
GATEWAY_URL="${GATEWAY_URL:-http://127.0.0.1:8080}"
curl -fsS "$GATEWAY_URL/healthz" >/dev/null || fail "gateway /healthz did not answer"
echo "  /healthz ok"
curl -fsS "$GATEWAY_URL/readyz" | tee /dev/stderr | grep -q '"ready":true' \
  || fail "gateway /readyz reports not ready"

step "gateway error contract"
# An unknown path under /v1 sits behind the same guard chain as a real one, so an
# unauthenticated probe is answered 401 and learns nothing about which routes exist. A
# caller who sent a valid key gets the 404 they expect from a typo. Both are problem+json.
code=$(curl -s -o /dev/null -w '%{http_code}' "$GATEWAY_URL/v1/nope")
[ "$code" = "401" ] || fail "unauthenticated unknown route returned $code, expected 401"
curl -s -D- -o /dev/null "$GATEWAY_URL/v1/nope" | grep -qi 'content-type: application/problem+json' \
  || fail "errors are not problem+json"
echo "  unknown route, no key -> 401 problem+json"

step "gateway synthesize"
# A throwaway tenant, created through the admin API the same way a real one is. The key
# is never echoed: smoke output ends up in CI logs.
ADMIN_KEY="${VITTS_ADMIN_KEY:-$(grep '^VITTS_ADMIN_KEY=' .env.example | cut -d= -f2-)}"
tenant=$(curl -fsS -X POST "$GATEWAY_URL/admin/v1/tenants" \
  -H "X-Admin-Key: $ADMIN_KEY" -H 'Content-Type: application/json' \
  -d '{"name":"smoke","plan_id":"free"}') || fail "could not create the smoke tenant"
KEY=$(printf '%s' "$tenant" | python3 -c 'import json,sys; print(json.load(sys.stdin)["key"]["secret"])')
[ -n "$KEY" ] || fail "tenant was created without a key"

post() { curl -sS -o "$2" -w '%{http_code}' -X POST "$GATEWAY_URL$1" \
  -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' -d "$3"; }

code=$(post /v1/synthesize /tmp/smoke-sync.wav '{"text":"Xin chào, đây là bản kiểm tra.","voice":"maichi","format":"wav"}')
[ "$code" = "200" ] || fail "sync synthesis returned $code"
bytes=$(wc -c < /tmp/smoke-sync.wav)
[ "$bytes" -gt 1000 ] || fail "sync synthesis returned $bytes bytes"
echo "  sync    -> 200, $bytes bytes of wav"

code=$(curl -sS -o /tmp/smoke-stream.bin -w '%{http_code}' -X POST "$GATEWAY_URL/v1/synthesize/stream" \
  -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"text":"Kiểm tra phát trực tuyến.","voice":"maichi","format":"wav"}')
[ "$code" = "200" ] || fail "streaming synthesis returned $code"
bytes=$(wc -c < /tmp/smoke-stream.bin)
[ "$bytes" -gt 1000 ] || fail "streaming synthesis returned $bytes bytes"
echo "  stream  -> 200, $bytes bytes"

post_idem() { curl -sS -o "$2" -w '%{http_code}' -X POST "$GATEWAY_URL$1" \
  -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -H "Idempotency-Key: smoke-$(date +%s)-$$" -d "$3"; }

code=$(post_idem /v1/jobs /tmp/smoke-job.json '{"text":"Một đoạn văn bản dài hơn để tạo công việc nền.","voice":"maichi","format":"mp3"}')
[ "$code" = "202" ] || { cat /tmp/smoke-job.json >&2; fail "job creation returned $code"; }
job=$(python3 -c 'import json;print(json.load(open("/tmp/smoke-job.json"))["id"])')
for _ in $(seq 60); do
  status=$(curl -fsS "$GATEWAY_URL/v1/jobs/$job" -H "Authorization: Bearer $KEY" \
    | python3 -c 'import json,sys;print(json.load(sys.stdin)["status"])')
  case "$status" in completed) break ;; failed) fail "job $job failed" ;; esac
  sleep 2
done
[ "$status" = "completed" ] || fail "job $job stalled in $status"
echo "  job     -> $status"

echo
echo "SMOKE OK"
