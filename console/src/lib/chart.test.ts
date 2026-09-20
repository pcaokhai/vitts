import { strict as assert } from "node:assert";
import { test } from "node:test";

import { densify, layoutBars, percentOf } from "./chart.ts";

// T-112. A run of zero-usage days must still read as days, not as missing data.
test("a zero day renders a visible mark", () => {
  const { bars } = layoutBars([
    { label: "1 Jan", value: 0 },
    { label: "2 Jan", value: 0 },
  ]);

  for (const bar of bars) {
    assert.ok(bar.height > 0, "a quiet day is still a day");
    assert.ok(bar.y < 180, "the mark sits inside the chart");
  }
});

test("the tallest bar fills the chart and the rest scale against it", () => {
  const { bars, peak } = layoutBars(
    [
      { label: "a", value: 50 },
      { label: "b", value: 100 },
    ],
    720,
    180,
  );

  const [small, tall] = bars;
  assert.ok(small && tall);
  assert.equal(peak, 100);
  assert.ok(tall.height > small.height);
  assert.ok(tall.height <= 180);
  assert.ok(Math.abs(tall.y) < 1, "the peak reaches the top");
});

test("bars never overlap", () => {
  const { bars } = layoutBars(Array.from({ length: 30 }, (_, i) => ({ label: `${i}`, value: i })));

  for (let i = 1; i < bars.length; i++) {
    const previous = bars[i - 1];
    const current = bars[i];
    assert.ok(previous && current);
    assert.ok(current.x >= previous.x + previous.width, `bar ${i} overlaps its neighbour`);
  }
});

test("an empty series does not divide by zero", () => {
  const chart = layoutBars([]);
  assert.deepEqual(chart.bars, []);
  assert.equal(chart.peak, 0);
});

// An over-quota tenant is a real state (ADR-011 lets usage overshoot during a Redis
// outage), so the meter must clamp rather than overflow its track.
test("the plan meter clamps above the allowance", () => {
  assert.equal(percentOf(150, 100), 100);
  assert.equal(percentOf(25, 100), 25);
  assert.equal(percentOf(10, 0), 0, "an unmetered plan is not an infinite bar");
});

// T-115. The API returns only days that had usage. Charting that sparse series made one
// busy day fill a 30-day range, reading as "every day was busy".
test("densify fills the days the API omits", () => {
  const series = densify("2026-03-01T00:00:00Z", "2026-03-05T00:00:00Z", [
    { day: "2026-03-03T00:00:00Z", chars: 500 },
  ]);

  assert.equal(series.length, 5);
  assert.deepEqual(
    series.map((point) => point.value),
    [0, 0, 500, 0, 0],
  );
  assert.equal(series[0]?.label, "2026-03-01");
  assert.equal(series[4]?.label, "2026-03-05");
});

test("densify spans a month boundary and a single day", () => {
  assert.equal(densify("2026-02-27T00:00:00Z", "2026-03-02T00:00:00Z", []).length, 4);
  assert.equal(densify("2026-03-02T00:00:00Z", "2026-03-02T00:00:00Z", []).length, 1);
});

test("densify does not spin on an inverted range", () => {
  assert.deepEqual(densify("2026-03-10T00:00:00Z", "2026-03-01T00:00:00Z", []), []);
});
