# 4. Limits and quota

Every refusal the API makes, and how fast it makes it.

## Limits and refusals

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

## Quota

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

---

[← Synthesis](03-synthesis.md) · [Index](README.md) · [Cache and usage →](05-cache-and-usage.md)
