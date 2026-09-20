// Chart geometry, kept away from React so it can be tested as the arithmetic it is.
//
// There is no chart library here on purpose: a daily bar series is a loop, and every
// charting default — the grid, the tooltip chrome, the palette — is exactly the
// template look the design rules forbid (docs/plans/3.5.md).

export interface Bar {
  /** x, y, width and height in the SVG's own coordinate space. */
  x: number;
  y: number;
  width: number;
  height: number;
  label: string;
  value: number;
}

export interface BarChart {
  bars: Bar[];
  /** peak is the value the tallest bar represents, for the axis label. */
  peak: number;
  width: number;
  height: number;
}

/**
 * densify turns the API's sparse series into one entry per day in the range.
 *
 * `GET /v1/usage` returns only days that had usage. Charting that directly makes a
 * single busy day fill the whole width of a 30-day range, which reads as "every day was
 * busy" — the exact opposite of the truth. The calendar is the x-axis, so the console
 * supplies the days the API left out.
 */
export function densify(
  from: string,
  to: string,
  days: { day: string; chars: number }[],
): { label: string; value: number }[] {
  const byDay = new Map(days.map((entry) => [entry.day.slice(0, 10), entry.chars]));
  const cursor = new Date(from.slice(0, 10) + "T00:00:00Z");
  const last = new Date(to.slice(0, 10) + "T00:00:00Z");

  const series: { label: string; value: number }[] = [];
  // Bounded so a malformed range cannot spin: a usage window is days, never years.
  for (let guard = 0; cursor <= last && guard < 400; guard++) {
    const label = cursor.toISOString().slice(0, 10);
    series.push({ label, value: byDay.get(label) ?? 0 });
    cursor.setUTCDate(cursor.getUTCDate() + 1);
  }
  return series;
}

/**
 * layoutBars maps a daily series onto a fixed viewBox.
 *
 * A zero-valued day still gets a visible sliver: a run of empty days should read as a
 * row of quiet marks, not as a gap that looks like missing data.
 */
export function layoutBars(
  values: { label: string; value: number }[],
  width = 720,
  height = 180,
  gap = 2,
): BarChart {
  const peak = values.reduce((max, point) => Math.max(max, point.value), 0);
  const slot = values.length > 0 ? width / values.length : width;
  const barWidth = Math.max(1, slot - gap);
  const minimum = 1.5;

  const bars = values.map((point, index) => {
    const scaled = peak > 0 ? (point.value / peak) * (height - minimum) : 0;
    const barHeight = scaled + (point.value > 0 ? minimum : minimum / 2);
    return {
      x: index * slot,
      y: height - barHeight,
      width: barWidth,
      height: barHeight,
      label: point.label,
      value: point.value,
    };
  });

  return { bars, peak, width, height };
}

/** percentOf is the plan meter, clamped so an over-quota tenant still renders a full bar. */
export function percentOf(used: number, allowance: number): number {
  if (allowance <= 0) {
    return 0;
  }
  return Math.min(100, (used / allowance) * 100);
}
