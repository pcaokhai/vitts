// Steady load at roughly 80% of one worker's capacity (T-14, NFR-01).
//
// A single worker serves one request at a time (ADR-003) at a measured RTF near 0.35, so
// arrivals are paced rather than piled on: the point is to hold utilisation high and see
// whether TTFA stays inside its budget, not to find the breaking point. That is spike.js.
import { sleep } from "k6";
import { stream, pickText } from "./lib.js";

// Arrival rate is expressed as whole requests per interval, because k6 requires an
// integer rate. One worker serves one request at a time (ADR-003) and a corpus utterance
// takes about 1.5-3 s depending on length, a single-worker box was measured to sustain
// 1 request per 4 s with zero rejections and to start refusing at 1 per 3 s
// (docs/reports/load-m3.md). The default is that measured rate; scale VITTS_RATE or
// shorten VITTS_PER in proportion to the worker count.
const RATE = Number(__ENV.VITTS_RATE || 1);
const PER = __ENV.VITTS_PER || "4s";

export const options = {
  scenarios: {
    steady: {
      executor: "constant-arrival-rate",
      rate: RATE,
      timeUnit: PER,
      duration: __ENV.VITTS_DURATION || "2m",
      preAllocatedVUs: 10,
      maxVUs: 40,
    },
  },
  thresholds: {
    // NFR-01: streaming TTFA p95 <= 300 ms, p99 <= 600 ms.
    "vitts_ttfa_ms": ["p(95)<300", "p(99)<600"],
    // Under steady load nothing should be refused; a rejection here means the rate is
    // above capacity and the run does not measure what it claims to.
    "vitts_rejected": ["count<1"],
    "vitts_failed": ["count<1"],
  },
};

export default function () {
  stream(pickText(0.2));
  sleep(0.1);
}
