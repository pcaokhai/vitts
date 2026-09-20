"use client";

import { useState } from "react";

import { ApiError, api, apiBaseUrl } from "@/lib/api";
import { useKey } from "@/components/KeyProvider";

/**
 * KeyGate is the console's entire sign-in.
 *
 * There are no console accounts: the API key is the identity, so "logging in" is
 * proving a key works. The proof is a real usage call rather than a shape check —
 * a key that parses but has been revoked should fail here, not two screens later.
 */
export function KeyGate() {
  const { signIn } = useKey();
  const [value, setValue] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [checking, setChecking] = useState(false);

  async function submit(event: React.FormEvent) {
    event.preventDefault();
    const candidate = value.trim();
    if (!candidate) {
      return;
    }

    setChecking(true);
    setError(null);
    try {
      await api.verify(candidate);
      signIn(candidate);
    } catch (cause) {
      setError(
        cause instanceof ApiError
          ? describe(cause)
          : `Could not reach ${apiBaseUrl}. Is the gateway running?`,
      );
    } finally {
      setChecking(false);
    }
  }

  return (
    <main className="gate">
      <div className="gate-panel">
        <p className="eyebrow">ViTTS Console</p>
        <h1 className="gate-title">
          Paste a key<span className="accent">.</span>
        </h1>
        <p className="gate-lede">
          The key is the account. It is held for this tab only, never stored on our
          servers, and you can revoke it from here.
        </p>

        <form onSubmit={submit} className="gate-form">
          <label htmlFor="key" className="label">
            API key
          </label>
          <input
            id="key"
            className="input mono"
            type="password"
            autoComplete="off"
            spellCheck={false}
            placeholder="zt_live_…"
            value={value}
            onChange={(event) => setValue(event.target.value)}
            aria-describedby={error ? "key-error" : undefined}
            aria-invalid={error ? true : undefined}
          />
          <button className="button button-primary" type="submit" disabled={checking || !value.trim()}>
            {checking ? "Checking…" : "Continue"}
          </button>
        </form>

        {error && (
          <p className="gate-error" id="key-error" role="alert">
            {error}
          </p>
        )}

        <p className="gate-foot mono">{apiBaseUrl}</p>
      </div>
    </main>
  );
}

/** describe turns the two failures a tenant actually hits into words about their key. */
function describe(error: ApiError): string {
  if (error.code === "unauthorized") {
    return "That key was not accepted. It may have been revoked, or belong to another environment.";
  }
  if (error.code === "forbidden_scope") {
    return "That key works, but it has no `usage` scope. Use a key with usage and keys scopes.";
  }
  return error.message;
}
