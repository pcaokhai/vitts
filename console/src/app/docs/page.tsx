import type { Metadata } from "next";

import { CodeTabs } from "@/components/CodeTabs";
import { apiBaseUrl } from "@/lib/api";
import { ENDPOINTS, QUICKSTART } from "@/lib/docs";

export const metadata: Metadata = {
  title: "ViTTS API docs",
  description: "Quickstart and reference for the ViTTS Vietnamese speech API.",
};

export default function DocsPage() {
  return (
    <article className="prose">
      <header className="doc-head">
        <p className="eyebrow">Documentation</p>
        <h1 className="doc-title">
          Vietnamese speech in one call<span className="accent">.</span>
        </h1>
        <p className="doc-lede">
          Per-character pricing, first audio in under 300&nbsp;ms, eight preset voices. The
          reference below is generated from the same OpenAPI document the gateway serves at{" "}
          <a className="link-accent mono" href={`${apiBaseUrl}/openapi.json`}>
            /openapi.json
          </a>
          .
        </p>
      </header>

      <section aria-labelledby="quickstart">
        <h2 id="quickstart" className="doc-h2">
          Quickstart
        </h2>
        <p className="doc-body">
          Create a key in the console, then pick a language. Every example speaks the same
          sentence and writes a WAV file.
        </p>
        <CodeTabs samples={QUICKSTART} />
      </section>

      <section aria-labelledby="errors">
        <h2 id="errors" className="doc-h2">
          Errors
        </h2>
        <p className="doc-body">
          Every failure is an RFC 9457 problem document with a stable <code>code</code>.
          Branch on the code, never on the message: the code is part of the contract, the
          message is prose for a person.
        </p>
        <dl className="error-list">
          <div className="error-row">
            <dt className="mono">quota_exceeded</dt>
            <dd>402. The month&apos;s character allowance is spent.</dd>
          </div>
          <div className="error-row">
            <dt className="mono">rate_limited</dt>
            <dd>429, with <code>Retry-After</code>. Both SDKs honour it for you.</dd>
          </div>
          <div className="error-row">
            <dt className="mono">overloaded</dt>
            <dd>
              503, with <code>Retry-After</code>. The fleet is full; the request was refused
              in milliseconds rather than accepted and timed out.
            </dd>
          </div>
          <div className="error-row">
            <dt className="mono">text_too_long</dt>
            <dd>413. Use a job for anything over 3,000 characters.</dd>
          </div>
          <div className="error-row">
            <dt className="mono">unknown_voice</dt>
            <dd>
              422. Call <code>/v1/voices</code>, which needs no key.
            </dd>
          </div>
        </dl>
      </section>

      <section aria-labelledby="reference">
        <h2 id="reference" className="doc-h2">
          Endpoints
        </h2>
        <table className="table endpoint-table">
          <thead>
            <tr>
              <th scope="col">Endpoint</th>
              <th scope="col">Scope</th>
              <th scope="col">What it does</th>
            </tr>
          </thead>
          <tbody>
            {ENDPOINTS.map((endpoint) => (
              <tr key={`${endpoint.method} ${endpoint.path}`}>
                <td className="mono endpoint-path">
                  <span className="method">{endpoint.method}</span> {endpoint.path}
                </td>
                <td className="mono scope-list">{endpoint.scope}</td>
                <td>{endpoint.summary}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </section>

      <section aria-labelledby="streaming">
        <h2 id="streaming" className="doc-h2">
          Streaming and cancellation
        </h2>
        <p className="doc-body">
          <code>/v1/synthesize/stream</code> sends audio as it is produced, in order.
          Hanging up cancels the worker within 200&nbsp;ms, and you are billed for the audio
          delivered rather than the text you sent. A WebSocket is available at the same path
          for callers that need to cancel mid-utterance, such as a voice bot handling
          barge-in.
        </p>
        <CodeTabs samples={QUICKSTART.map((sample) => ({ ...sample, code: sample.streamCode }))} />
      </section>
    </article>
  );
}
