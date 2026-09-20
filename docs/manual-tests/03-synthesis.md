# 3. Synthesis

Audio you listen to: sync, streaming, cancellation and the WebSocket.

## Synchronous synthesis

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

## Streaming

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

---

[← Health and voices](02-health-and-voices.md) · [Index](README.md) · [Limits and quota →](04-limits-and-quota.md)
