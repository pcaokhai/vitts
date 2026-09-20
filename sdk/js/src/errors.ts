import type { components } from "./types.gen.ts";

export type Problem = components["schemas"]["Problem"];

/**
 * VittsError is every refusal the API makes, with the reason it gave.
 *
 * The gateway answers RFC 9457 problem documents with a stable `code` (ADR-009), so
 * branching on `error.code` is part of the contract and safe to write against —
 * unlike the message, which is prose.
 */
export class VittsError extends Error {
  readonly code: string;
  readonly status: number;
  readonly requestId: string | undefined;
  /** retryAfterMs is set when the API said when to come back (429, 503). */
  readonly retryAfterMs: number | undefined;

  constructor(problem: Problem, status: number, retryAfterMs?: number) {
    super(problem.detail?.trim() || problem.title || `HTTP ${status}`);
    this.name = "VittsError";
    this.code = problem.code ?? "unknown";
    this.status = status;
    this.requestId = problem.request_id;
    this.retryAfterMs = retryAfterMs;
  }

  /** retryable is true for the two states the API tells you to wait out. */
  get retryable(): boolean {
    return this.status === 429 || this.status === 503;
  }
}

/** VittsTransportError is a failure to reach the API at all: DNS, TLS, a dropped socket. */
export class VittsTransportError extends Error {
  constructor(cause: unknown) {
    super(cause instanceof Error ? cause.message : String(cause));
    this.name = "VittsTransportError";
    this.cause = cause;
  }
}
