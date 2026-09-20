# 6. Jobs

Long text: segmentation, merge, idempotency and cancel.

## Jobs

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

---

[← Cache and usage](05-cache-and-usage.md) · [Index](README.md) · [Isolation, keys and admin →](07-isolation-and-keys.md)
