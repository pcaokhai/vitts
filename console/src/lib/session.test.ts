import { strict as assert } from "node:assert";
import { test } from "node:test";

import { maskKey } from "./session.ts";

// A masked key must be recognisable and useless: enough to tell two keys apart, never
// enough to reconstruct one.
test("maskKey keeps the prefix and the last four characters", () => {
  const masked = maskKey("zt_live_abcdefghijklmnopqrstuvwxyz0123");

  assert.ok(masked.startsWith("zt_live_"));
  assert.ok(masked.endsWith("0123"));
  assert.ok(!masked.includes("abcdefghij"), "the secret body is never shown");
});

test("maskKey does not fall apart on an unexpected shape", () => {
  assert.ok(maskKey("short").length > 0);
});
