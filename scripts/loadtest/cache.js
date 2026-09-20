// Cache-heavy profile (ADR-006).
//
// IVR and soundbox tenants repeat phrases constantly, which is the assumption the cache
// is built on (A4). This run checks the hit ratio is high and that a hit is fast enough
// to be worth having.
import { synthesize, pickText } from "./lib.js";

export const options = {
  scenarios: {
    cached: {
      executor: "constant-arrival-rate",
      rate: Number(__ENV.VITTS_RPS || 8),
      timeUnit: "1s",
      duration: __ENV.VITTS_DURATION || "1m",
      preAllocatedVUs: 20,
      maxVUs: 60,
    },
  },
  thresholds: {
    // A repeat-heavy profile should mostly hit; the first request of each phrase misses.
    "vitts_cache_hit": ["rate>0.8"],
    // A hit is an object read plus a header, so it should be far faster than synthesis.
    // Misses are excluded on purpose: they measure the model, not the cache.
    "vitts_hit_ms": ["p(95)<250"],
    "vitts_failed": ["count<1"],
  },
};

export default function () {
  // 90% repeats: the shape of an IVR menu rather than of a text-to-speech demo.
  synthesize(pickText(0.9));
}
