// Three times capacity (T-13, NFR-05, ADR-007).
//
// The claim under test is not that everything succeeds - it cannot - but that what fails
// fails *fast*, with Retry-After, and that streams keep being served while batch work is
// squeezed out.
import { stream, synthesize, pickText, rejectLatency } from "./lib.js";

const TARGET_RPS = Number(__ENV.VITTS_RPS || 6);

export const options = {
  scenarios: {
    spike: {
      executor: "constant-arrival-rate",
      rate: TARGET_RPS,
      timeUnit: "1s",
      duration: __ENV.VITTS_DURATION || "1m",
      preAllocatedVUs: 30,
      maxVUs: 120,
    },
  },
  thresholds: {
    // US-12 acceptance criterion 1: p95 time to a refusal under 50 ms.
    "vitts_reject_ms": ["p(95)<50"],
    // Nothing may fail in a way that is neither served nor a clean refusal.
    "vitts_failed": ["count<1"],
  },
};

export default function () {
  // A mix, because the reservation only matters when both classes compete.
  if (Math.random() < 0.7) {
    stream(pickText(0.1));
  } else {
    synthesize(pickText(0.1));
  }
}
