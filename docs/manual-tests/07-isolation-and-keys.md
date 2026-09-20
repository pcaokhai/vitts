# 7. Isolation, keys and admin

The security surface. The isolation check is the most important one in the repo.

## Tenant isolation

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

## Key management

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

## Admin surface

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

---

[← Jobs](06-jobs.md) · [Index](README.md) · [Console and SDKs →](08-console-and-sdks.md)
