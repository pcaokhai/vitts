# 5. Cache and usage

What a repeated request costs, and whether the ledger agrees.

## Cache

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

## Usage report

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

---

[← Limits and quota](04-limits-and-quota.md) · [Index](README.md) · [Jobs →](06-jobs.md)
