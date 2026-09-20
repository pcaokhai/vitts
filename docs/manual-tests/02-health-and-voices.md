# 2. Health and voices

The two endpoints that answer before any synthesis happens, and the one that needs no key.

## Health and readiness

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

## Voices

```bash
curl -s $GATEWAY/v1/voices | python3 -m json.tool | head -20
curl -s $GATEWAY/v1/voices -H "Authorization: Bearer $KEY" | python3 -c 'import json,sys; print(len(json.load(sys.stdin)), "voices")'
```

**Pass:** eight voices **both with and without a key**. US-13 makes the catalogue public;
a key only adds that tenant's own voices.

---

---

[← Setup](01-setup.md) · [Index](README.md) · [Synthesis →](03-synthesis.md)
