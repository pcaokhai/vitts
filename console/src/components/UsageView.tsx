"use client";

import { useEffect, useState } from "react";

import { ApiError, api, type UsageReport } from "@/lib/api";
import { densify, layoutBars, percentOf } from "@/lib/chart";
import { formatCount, formatDay, formatDuration, formatExact, ratio } from "@/lib/format";
import { useKey } from "@/components/KeyProvider";

const RANGES = [
  { days: 7, label: "7 days" },
  { days: 30, label: "30 days" },
  { days: 90, label: "90 days" },
];

export function UsageView() {
  const { key, signOut } = useKey();
  const [days, setDays] = useState(30);
  const [report, setReport] = useState<UsageReport | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);

  // The guard is not ceremony: switching range twice quickly starts two requests, and
  // without it the slower one can land last and show the wrong period.
  useEffect(() => {
    if (!key) return;
    let cancelled = false;

    void (async () => {
      try {
        const next = await api.usage(key, days);
        if (!cancelled) {
          setReport(next);
          setError(null);
        }
      } catch (cause) {
        if (cancelled) return;
        // A key revoked in another tab, or from this console, must not leave the
        // session pretending to be signed in.
        if (cause instanceof ApiError && cause.status === 401) {
          signOut();
          return;
        }
        setError(cause instanceof ApiError ? cause.message : "Could not reach the gateway.");
      } finally {
        if (!cancelled) setLoading(false);
      }
    })();

    return () => {
      cancelled = true;
    };
  }, [days, key, signOut]);

  async function downloadCsv() {
    if (!key) return;
    setBusy(true);
    try {
      const csv = await api.usageCsv(key, days);
      const url = URL.createObjectURL(new Blob([csv], { type: "text/csv" }));
      const link = document.createElement("a");
      link.href = url;
      link.download = `vitts-usage-${days}d.csv`;
      link.click();
      URL.revokeObjectURL(url);
    } catch (cause) {
      setError(cause instanceof ApiError ? cause.message : "Could not download the report.");
    } finally {
      setBusy(false);
    }
  }

  if (error) {
    return <p className="notice notice-error" role="alert">{error}</p>;
  }
  if (!report) {
    return <p className="notice mono">{loading ? "Reading usage…" : "No usage yet."}</p>;
  }

  const used = report.totals.chars;
  const allowance = report.plan_chars_per_month;
  const meter = percentOf(used, allowance);
  const over = allowance > 0 && used > allowance;
  const series = densify(report.from, report.to, report.days);
  const chart = layoutBars(series);

  return (
    <>
      <header className="section-head">
        <div>
          <p className="eyebrow">Characters synthesized</p>
          <p className="figure mono">
            {formatCount(used)}
            {allowance > 0 && (
              <span className="figure-of"> / {formatCount(allowance)}</span>
            )}
          </p>
        </div>

        <div className="range" role="group" aria-label="Date range">
          {RANGES.map((range) => (
            <button
              key={range.days}
              type="button"
              className={`range-option${range.days === days ? " is-current" : ""}`}
              aria-pressed={range.days === days}
              onClick={() => {
                setLoading(true);
                setDays(range.days);
              }}
            >
              {range.label}
            </button>
          ))}
        </div>
      </header>

      {allowance > 0 && (
        <div className="meter" role="img" aria-label={`${Math.round(meter)} percent of the monthly allowance used`}>
          <div className={`meter-fill${over ? " is-over" : ""}`} style={{ inlineSize: `${meter}%` }} />
          <p className="meter-note">
            <span className="mono">{Math.round(meter)}%</span> of the monthly allowance
            {over && <span className="over-tag"> — over allowance</span>}
          </p>
        </div>
      )}

      <figure className="chart">
        <svg
          viewBox={`0 0 ${chart.width} ${chart.height}`}
          preserveAspectRatio="none"
          className="chart-svg"
          role="img"
          aria-label={`Daily characters, peak ${formatExact(chart.peak)}`}
        >
          {chart.bars.map((bar) => (
            <rect
              key={bar.label}
              x={bar.x}
              y={bar.y}
              width={bar.width}
              height={bar.height}
              className={bar.value > 0 ? "bar" : "bar is-empty"}
            >
              <title>{`${formatDay(bar.label + "T00:00:00Z")} — ${formatExact(bar.value)} characters`}</title>
            </rect>
          ))}
        </svg>
        <figcaption className="chart-axis mono">
          <span>{formatDay(report.from)}</span>
          <span className="chart-peak">peak {formatCount(chart.peak)}</span>
          <span>{formatDay(report.to)}</span>
        </figcaption>
      </figure>

      <dl className="stats">
        <Stat label="Requests" value={formatExact(report.totals.requests)} />
        <Stat label="Audio produced" value={formatDuration(report.totals.audio_ms)} />
        <Stat
          label="Served from cache"
          value={ratio(report.totals.cache_hits, report.totals.requests)}
          note={`${formatExact(report.totals.cache_hits)} requests`}
        />
      </dl>

      <p className="download">
        <button type="button" className="link-accent mono" onClick={downloadCsv} disabled={busy}>
          {busy ? "preparing…" : "usage.csv"}
        </button>{" "}
        <span className="hint">{formatDay(report.from)} to {formatDay(report.to)}</span>
      </p>
    </>
  );
}

function Stat({ label, value, note }: { label: string; value: string; note?: string }) {
  return (
    <div className="stat">
      <dt className="stat-label">{label}</dt>
      <dd className="stat-value mono">{value}</dd>
      {note && <dd className="stat-note mono">{note}</dd>}
    </div>
  );
}
