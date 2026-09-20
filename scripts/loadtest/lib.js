// Shared helpers for the k6 scenarios (US-20).
//
// Every scenario measures the same things, so a comparison across runs is meaningful:
// TTFA from the streaming endpoint, wall time for sync, and how fast a refusal comes
// back. Those three are what NFR-01, NFR-02 and NFR-05 promise.
import http from "k6/http";
import { Trend, Rate, Counter } from "k6/metrics";

export const BASE = __ENV.VITTS_URL || "http://127.0.0.1:8080";
export const KEY = __ENV.VITTS_KEY;

if (!KEY) {
  throw new Error("VITTS_KEY is required: create a tenant and pass its secret");
}

// Custom metrics. k6's built-ins measure HTTP; these measure the promises.
export const ttfa = new Trend("vitts_ttfa_ms", true);
export const syncLatency = new Trend("vitts_sync_ms", true);
// Hits and misses are different operations - one reads an object, the other runs a
// model - so mixing them in one trend measures neither. The split is what makes a
// threshold on hit latency meaningful.
export const hitLatency = new Trend("vitts_hit_ms", true);
export const missLatency = new Trend("vitts_miss_ms", true);
export const rejectLatency = new Trend("vitts_reject_ms", true);
export const cacheHits = new Rate("vitts_cache_hit");
export const rejected = new Counter("vitts_rejected");
export const failed = new Counter("vitts_failed");

// A corpus with a deliberate mix of lengths: one short phrase dominates so the cache has
// something to hit, and longer ones keep the workers honest.
export const CORPUS = [
  "Xin chào, cảm ơn bạn đã gọi.",
  "Vui lòng giữ máy, nhân viên sẽ hỗ trợ bạn ngay.",
  "Đơn hàng của bạn đã được xác nhận và sẽ giao trong hai ngày làm việc.",
  "Hôm nay trời đẹp, chúng ta cùng đi dạo phố cổ Hà Nội nhé.",
  "Công ty chúng tôi cung cấp dịch vụ tổng hợp giọng nói tiếng Việt chất lượng cao cho doanh nghiệp.",
];

export function headers() {
  return {
    Authorization: `Bearer ${KEY}`,
    "Content-Type": "application/json",
  };
}

// pickText returns a phrase. `repeatRatio` of calls return the same one, which is how a
// cache-heavy profile is expressed without pretending every request is unique.
export function pickText(repeatRatio) {
  if (repeatRatio && Math.random() < repeatRatio) {
    return CORPUS[0];
  }
  // Unique suffix so the rest genuinely miss the cache.
  const base = CORPUS[Math.floor(Math.random() * CORPUS.length)];
  return `${base} ${__VU}-${__ITER}`;
}

export function body(text, voice = "maichi") {
  return JSON.stringify({ text, voice });
}

// synthesize measures a complete request (NFR-02).
export function synthesize(text) {
  const started = Date.now();
  const res = http.post(`${BASE}/v1/synthesize`, body(text), {
    headers: headers(),
    tags: { endpoint: "sync" },
  });

  record(res, () => syncLatency.add(Date.now() - started));
  return res;
}

// stream measures time to first byte, which for the streaming endpoint is the RIFF
// header immediately followed by the first audio frame (NFR-01).
export function stream(text) {
  const res = http.post(`${BASE}/v1/synthesize/stream`, body(text), {
    headers: headers(),
    tags: { endpoint: "stream" },
  });

  record(res, () => ttfa.add(res.timings.waiting));
  return res;
}

// record classifies one response. A 429 or 503 is not a failure: NFR-05 says overload
// must be refused quickly, so the refusal's latency is the measurement.
function record(res, onSuccess) {
  if (res.status === 200) {
    onSuccess();
    const hit = res.headers["X-Cache"] === "HIT";
    cacheHits.add(hit);
    (hit ? hitLatency : missLatency).add(res.timings.duration);
    return;
  }
  if (res.status === 429 || res.status === 503) {
    rejected.add(1);
    rejectLatency.add(res.timings.duration);
    return;
  }
  failed.add(1);
}
