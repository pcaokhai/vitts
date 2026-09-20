import { strict as assert } from "node:assert";
import { test } from "node:test";

import { ApiError, isoDate, usageRange } from "./api.ts";

// T-113. The gateway's own explanation is what the tenant can act on, so it must be
// what reaches the screen.
test("ApiError carries the gateway's detail, not a generic message", () => {
  const error = new ApiError({
    code: "quota_exceeded",
    title: "Quota exceeded",
    detail: "used 51000 of 50000",
    status: 402,
    request_id: "req-1",
  });

  assert.equal(error.message, "used 51000 of 50000");
  assert.equal(error.code, "quota_exceeded");
  assert.equal(error.status, 402);
  assert.equal(error.requestId, "req-1");
});

test("ApiError falls back to the title when there is no detail", () => {
  const error = new ApiError({ code: "unauthorized", title: "Unauthorized", status: 401 });
  assert.equal(error.message, "Unauthorized");
});

test("usageRange covers n days inclusive of today", () => {
  const today = new Date("2026-03-10T09:00:00Z");

  assert.equal(usageRange(7, today), "from=2026-03-04&to=2026-03-10");
  assert.equal(usageRange(1, today), "from=2026-03-10&to=2026-03-10");
});

test("usageRange crosses a month boundary", () => {
  assert.equal(usageRange(3, new Date("2026-03-02T00:00:00Z")), "from=2026-02-28&to=2026-03-02");
});

test("isoDate is date-only and UTC", () => {
  assert.equal(isoDate(new Date("2026-03-10T23:30:00Z")), "2026-03-10");
});
