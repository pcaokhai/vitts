// Presentation of the figures the API returns.

const compact = new Intl.NumberFormat("en", { notation: "compact", maximumFractionDigits: 1 });
const plain = new Intl.NumberFormat("en");

export function formatCount(value: number): string {
  return value < 10_000 ? plain.format(value) : compact.format(value);
}

export function formatExact(value: number): string {
  return plain.format(value);
}

/** formatDuration turns the API's milliseconds into something a person reads. */
export function formatDuration(ms: number): string {
  const seconds = Math.round(ms / 1000);
  if (seconds < 60) {
    return `${seconds}s`;
  }
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) {
    return `${minutes}m ${seconds % 60}s`;
  }
  return `${Math.floor(minutes / 60)}h ${minutes % 60}m`;
}

/** formatDay takes the API's RFC 3339 timestamps and reads them as the UTC day they are. */
export function formatDay(iso: string): string {
  return new Date(iso).toLocaleDateString("en", {
    day: "numeric",
    month: "short",
    timeZone: "UTC",
  });
}

export function formatDateTime(iso: string): string {
  return new Date(iso).toLocaleString("en", { dateStyle: "medium", timeStyle: "short" });
}

/** ratio is the cache hit rate, which is only meaningful against a non-zero denominator. */
export function ratio(part: number, whole: number): string {
  return whole > 0 ? `${Math.round((part / whole) * 100)}%` : "—";
}
