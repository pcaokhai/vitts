// The console's whole relationship with the gateway. Every call here is one a tenant
// could make with curl: the console holds no credential of its own (ADR-012).

/** Problem is RFC 9457, the only error shape the gateway emits. */
export interface Problem {
  code: string;
  title: string;
  detail?: string;
  status: number;
  request_id?: string;
}

/**
 * ApiError carries the gateway's own words.
 *
 * The API states plainly why it refused — "used 51000 of 50000", "key has no such
 * scope" — and replacing that with a generic message would throw away the only
 * explanation the tenant can act on.
 */
export class ApiError extends Error {
  readonly code: string;
  readonly status: number;
  readonly requestId?: string;

  constructor(problem: Problem) {
    super(problem.detail?.trim() || problem.title);
    this.name = "ApiError";
    this.code = problem.code;
    this.status = problem.status;
    this.requestId = problem.request_id;
  }
}

export interface UsageTotals {
  chars: number;
  audio_ms: number;
  requests: number;
  cache_hits: number;
}

export interface UsageDay extends UsageTotals {
  /** RFC 3339, midnight UTC. The API returns a timestamp, not a date-only string. */
  day: string;
}

/** UsageReport is docs/api/openapi.yaml § UsageReport, pinned by T-114. */
export interface UsageReport {
  from: string;
  to: string;
  days: UsageDay[];
  totals: UsageTotals;
  plan_chars_per_month: number;
}

export interface ApiKey {
  id: string;
  name: string;
  prefix: string;
  scopes: string[];
  created_at: string;
}

// Deliberately absent: last_used_at, which the gateway does not record, and revoked_at,
// which never arrives — GET /v1/keys returns live keys only. Modelling either would be
// modelling a field the API has no way to send.

/** CreatedKey is the only response that ever carries a secret (US-17 AC-1). */
export interface CreatedKey extends ApiKey {
  secret: string;
}

const baseUrl = (process.env.NEXT_PUBLIC_VITTS_API_URL ?? "http://127.0.0.1:8080").replace(/\/+$/, "");

/** apiBaseUrl is exported so the key screen can tell the tenant which gateway it is talking to. */
export const apiBaseUrl = baseUrl;

async function request<T>(key: string, path: string, init: RequestInit = {}): Promise<T> {
  const headers = new Headers(init.headers);
  headers.set("Authorization", `Bearer ${key}`);
  if (init.body !== undefined) {
    headers.set("Content-Type", "application/json");
  }

  const response = await fetch(`${baseUrl}${path}`, { ...init, headers });

  if (!response.ok) {
    throw new ApiError(await readProblem(response));
  }
  if (response.status === 204) {
    return undefined as T;
  }
  return (await response.json()) as T;
}

/**
 * readProblem never throws.
 *
 * A gateway that is down, or a proxy in front of it, answers with HTML or nothing at
 * all, and a parse failure there would surface as "Unexpected token <" instead of
 * something the tenant can read.
 */
async function readProblem(response: Response): Promise<Problem> {
  try {
    const body = (await response.json()) as Partial<Problem>;
    if (typeof body?.code === "string") {
      return {
        code: body.code,
        title: body.title ?? "Request failed",
        detail: body.detail,
        status: response.status,
        request_id: body.request_id,
      };
    }
  } catch {
    // fall through to the generic shape below
  }

  return {
    code: "unreachable",
    title: `The gateway answered ${response.status}`,
    detail: "The response was not a problem document. The API may be down or proxied.",
    status: response.status,
  };
}

export const api = {
  /** verify is the console's "login": the cheapest call that proves a key works. */
  verify: (key: string) => request<UsageReport>(key, `/v1/usage?${usageRange(7)}`),
  usage: (key: string, days: number) => request<UsageReport>(key, `/v1/usage?${usageRange(days)}`),
  /**
   * usageCsv fetches rather than linking.
   *
   * A plain <a download> cannot carry the bearer header, so it would hand the tenant a
   * 401 problem document named usage.csv. The key never belongs in the query string
   * either: that lands in proxy and browser history (US-16 AC-3).
   */
  usageCsv: async (key: string, days: number): Promise<string> => {
    const response = await fetch(`${baseUrl}/v1/usage?${usageRange(days)}&format=csv`, {
      headers: { Authorization: `Bearer ${key}` },
    });
    if (!response.ok) {
      throw new ApiError(await readProblem(response));
    }
    return response.text();
  },
  listKeys: (key: string) => request<ApiKey[]>(key, "/v1/keys"),
  createKey: (key: string, name: string, scopes: string[]) =>
    request<CreatedKey>(key, "/v1/keys", {
      method: "POST",
      body: JSON.stringify({ name, scopes }),
    }),
  revokeKey: (key: string, id: string) =>
    request<void>(key, `/v1/keys/${encodeURIComponent(id)}`, { method: "DELETE" }),
};

/** usageRange builds the from/to pair for the last n days, inclusive of today. */
export function usageRange(days: number, today = new Date()): string {
  const to = new Date(today);
  const from = new Date(today);
  from.setUTCDate(from.getUTCDate() - (days - 1));
  return `from=${isoDate(from)}&to=${isoDate(to)}`;
}

export function isoDate(date: Date): string {
  return date.toISOString().slice(0, 10);
}
