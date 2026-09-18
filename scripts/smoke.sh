#!/usr/bin/env bash
# End-to-end check against the local stack. Task 0.6 covers the worker leg (Health and
# a streamed synthesis); the gateway legs (sync, stream, job) are added by 1.9/1.10/2.3.
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
code=$(curl -s -o /dev/null -w '%{http_code}' "$GATEWAY_URL/v1/nope")
[ "$code" = "404" ] || fail "unknown route returned $code, expected 404"
curl -s -D- -o /dev/null "$GATEWAY_URL/v1/nope" | grep -qi 'content-type: application/problem+json' \
  || fail "errors are not problem+json"
echo "  unknown route -> 404 problem+json"

step "gateway synthesize"
echo "  ..  sync, stream and job legs land in tasks 1.9, 1.10 and 2.3"

echo
echo "SMOKE OK"
