import { VittsError, VittsTransportError, type Problem } from "./errors.ts";
import type { components } from "./types.gen.ts";

type Schemas = components["schemas"];

/**
 * Optional makes the fields the *server* defaults optional for the caller.
 *
 * openapi-typescript marks a property with a `default` as required, which is right for a
 * response — the server always sends one — and wrong for a request, where the whole point
 * of a default is that you may leave it out. Only optionality is adjusted here; the
 * shape and the value types stay exactly as generated.
 */
type Optional<T, K extends keyof T> = Omit<T, K> & Partial<Pick<T, K>>;

type Defaulted = "voice" | "format" | "sample_rate" | "normalize";

export type SynthesizeRequest = Optional<Schemas["SynthesizeRequest"], Defaulted>;
export type JobCreateRequest = Optional<Schemas["JobCreateRequest"], Defaulted>;
export type Job = Schemas["Job"];
export type JobStatus = Schemas["JobStatus"];
export type Voice = Schemas["Voice"];
export type ApiKey = Schemas["ApiKey"];

export interface Audio {
  /** The audio itself, in the format that was asked for. */
  bytes: Uint8Array;
  /** The Content-Type the API sent, e.g. "audio/mpeg". */
  contentType: string;
}

export interface ClientOptions {
  apiKey: string;
  /** baseUrl of the gateway. No trailing slash needed. */
  baseUrl?: string;
  /** fetch to use. Injected for tests and for runtimes with their own implementation. */
  fetch?: typeof globalThis.fetch;
  /**
   * maxRetries for 429 and 503 only. The API always says when to come back, so the wait
   * is its Retry-After rather than a guess. Zero disables retrying.
   */
  maxRetries?: number;
  /** Per-request timeout. Streaming requests are exempt: they are long by nature. */
  timeoutMs?: number;
}

const DEFAULT_BASE_URL = "http://127.0.0.1:8080";
const DEFAULT_MAX_RETRIES = 2;
const DEFAULT_TIMEOUT_MS = 30_000;
/** A server's Retry-After is honoured up to this, past which failing fast is kinder. */
const MAX_RETRY_WAIT_MS = 30_000;

export class VittsClient {
  readonly #apiKey: string;
  readonly #baseUrl: string;
  readonly #fetch: typeof globalThis.fetch;
  readonly #maxRetries: number;
  readonly #timeoutMs: number;

  constructor(options: ClientOptions) {
    if (!options.apiKey) {
      throw new Error("apiKey is required");
    }
    this.#apiKey = options.apiKey;
    this.#baseUrl = (options.baseUrl ?? DEFAULT_BASE_URL).replace(/\/+$/, "");
    this.#fetch = options.fetch ?? globalThis.fetch.bind(globalThis);
    this.#maxRetries = options.maxRetries ?? DEFAULT_MAX_RETRIES;
    this.#timeoutMs = options.timeoutMs ?? DEFAULT_TIMEOUT_MS;
  }

  /** synthesize returns a complete audio file. Use stream() when latency matters. */
  async synthesize(request: SynthesizeRequest, signal?: AbortSignal): Promise<Audio> {
    const response = await this.#send("/v1/synthesize", {
      method: "POST",
      body: JSON.stringify(request),
      signal,
    });
    return {
      bytes: new Uint8Array(await response.arrayBuffer()),
      contentType: response.headers.get("content-type") ?? "application/octet-stream",
    };
  }

  /**
   * stream yields audio as it is produced, in order.
   *
   * Aborting the signal stops the request, and the gateway cancels the worker within
   * 200 ms (US-01). You are billed for the audio delivered, not the text sent (ADR-010).
   */
  async *stream(
    request: SynthesizeRequest,
    signal?: AbortSignal,
  ): AsyncGenerator<Uint8Array, void, undefined> {
    // No timeout: a stream is long by design, and the caller's signal is the control.
    const response = await this.#send("/v1/synthesize/stream", {
      method: "POST",
      body: JSON.stringify(request),
      signal,
      noTimeout: true,
      // Retrying mid-stream would replay audio the caller already has.
      noRetry: true,
    });

    const body = response.body;
    if (!body) {
      throw new VittsTransportError("the streaming response had no body");
    }

    const reader = body.getReader();
    try {
      for (;;) {
        const { done, value } = await reader.read();
        if (done) return;
        if (value) yield value;
      }
    } finally {
      // Releases the socket when the caller breaks out of the loop early.
      await reader.cancel().catch(() => undefined);
    }
  }

  async listVoices(signal?: AbortSignal): Promise<Voice[]> {
    return this.#json<Voice[]>("/v1/voices", { method: "GET", signal });
  }

  async usage(from: string, to: string, signal?: AbortSignal): Promise<Schemas["UsageReport"]> {
    const query = new URLSearchParams({ from, to });
    return this.#json<Schemas["UsageReport"]>(`/v1/usage?${query}`, { method: "GET", signal });
  }

  /**
   * createJob starts long-text synthesis.
   *
   * idempotencyKey is required by the API and by good sense: a retried create must not
   * produce a second job (US-14). One is generated when you do not supply one.
   */
  async createJob(
    request: JobCreateRequest,
    idempotencyKey: string = crypto.randomUUID(),
    signal?: AbortSignal,
  ): Promise<Job> {
    return this.#json<Job>("/v1/jobs", {
      method: "POST",
      body: JSON.stringify(request),
      headers: { "Idempotency-Key": idempotencyKey },
      signal,
    });
  }

  async getJob(id: string, signal?: AbortSignal): Promise<Job> {
    return this.#json<Job>(`/v1/jobs/${encodeURIComponent(id)}`, { method: "GET", signal });
  }

  async listJobs(signal?: AbortSignal): Promise<Job[]> {
    return this.#json<Job[]>("/v1/jobs", { method: "GET", signal });
  }

  async cancelJob(id: string, signal?: AbortSignal): Promise<void> {
    await this.#send(`/v1/jobs/${encodeURIComponent(id)}`, { method: "DELETE", signal });
  }

  /**
   * waitForJob polls until the job reaches a terminal state.
   *
   * It gives up rather than looping forever: a caller that wants to wait all day can say
   * so, but the default is a bounded wait that returns control.
   */
  async waitForJob(
    id: string,
    options: { pollMs?: number; timeoutMs?: number; signal?: AbortSignal } = {},
  ): Promise<Job> {
    const pollMs = options.pollMs ?? 2_000;
    const deadline = Date.now() + (options.timeoutMs ?? 10 * 60_000);

    for (;;) {
      const job = await this.getJob(id, options.signal);
      if (job.status === "completed" || job.status === "failed" || job.status === "cancelled") {
        return job;
      }
      if (Date.now() + pollMs > deadline) {
        throw new Error(`job ${id} was still ${job.status} after the timeout`);
      }
      await sleep(pollMs, options.signal);
    }
  }

  async listKeys(signal?: AbortSignal): Promise<ApiKey[]> {
    return this.#json<ApiKey[]>("/v1/keys", { method: "GET", signal });
  }

  async #json<T>(path: string, init: SendInit): Promise<T> {
    const response = await this.#send(path, init);
    return (await response.json()) as T;
  }

  async #send(path: string, init: SendInit): Promise<Response> {
    let attempt = 0;

    for (;;) {
      const headers = new Headers(init.headers);
      headers.set("Authorization", `Bearer ${this.#apiKey}`);
      if (init.body !== undefined) headers.set("Content-Type", "application/json");

      const timeout = init.noTimeout ? undefined : AbortSignal.timeout(this.#timeoutMs);
      const signal = combineSignals(init.signal, timeout);

      let response: Response;
      try {
        response = await this.#fetch(`${this.#baseUrl}${path}`, {
          method: init.method,
          headers,
          body: init.body,
          signal,
        });
      } catch (cause) {
        // A caller's abort is their decision, not a transport failure to wrap.
        if (init.signal?.aborted) throw cause;
        throw new VittsTransportError(cause);
      }

      if (response.ok) return response;

      const retryAfterMs = readRetryAfter(response);
      const error = new VittsError(await readProblem(response), response.status, retryAfterMs);

      const canRetry =
        !init.noRetry && error.retryable && attempt < this.#maxRetries && !init.signal?.aborted;
      if (!canRetry) throw error;

      attempt += 1;
      await sleep(Math.min(retryAfterMs ?? 1_000, MAX_RETRY_WAIT_MS), init.signal);
    }
  }
}

interface SendInit {
  method: string;
  body?: string;
  headers?: Record<string, string>;
  signal?: AbortSignal;
  noTimeout?: boolean;
  noRetry?: boolean;
}

/**
 * readProblem never throws: a proxy or a dead gateway answers with HTML, and a parse
 * failure there would surface as a JSON syntax error instead of the status.
 */
async function readProblem(response: Response): Promise<Problem> {
  try {
    const body = (await response.json()) as Partial<Problem>;
    if (typeof body?.code === "string") return body as Problem;
  } catch {
    // fall through
  }
  return {
    type: "about:blank",
    title: `HTTP ${response.status}`,
    status: response.status,
    code: "unknown" as Problem["code"],
  } as Problem;
}

function readRetryAfter(response: Response): number | undefined {
  const raw = response.headers.get("retry-after");
  if (!raw) return undefined;
  const seconds = Number(raw);
  return Number.isFinite(seconds) ? seconds * 1_000 : undefined;
}

function combineSignals(...signals: (AbortSignal | undefined)[]): AbortSignal | undefined {
  const present = signals.filter((signal): signal is AbortSignal => signal !== undefined);
  if (present.length === 0) return undefined;
  if (present.length === 1) return present[0];
  return AbortSignal.any(present);
}

function sleep(ms: number, signal?: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    if (signal?.aborted) {
      reject(signal.reason);
      return;
    }
    const timer = setTimeout(() => {
      signal?.removeEventListener("abort", onAbort);
      resolve();
    }, ms);
    function onAbort() {
      clearTimeout(timer);
      reject(signal?.reason);
    }
    signal?.addEventListener("abort", onAbort, { once: true });
  });
}
