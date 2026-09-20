"use client";

import { useCallback, useEffect, useState } from "react";

import { ApiError, api, type ApiKey, type CreatedKey } from "@/lib/api";
import { formatDateTime } from "@/lib/format";
import { useKey } from "@/components/KeyProvider";

const SCOPES = ["synth", "jobs", "usage", "keys"] as const;

export function KeysView() {
  const { key, signOut } = useKey();
  const [keys, setKeys] = useState<ApiKey[] | null>(null);
  const [created, setCreated] = useState<CreatedKey | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [name, setName] = useState("");
  const [scopes, setScopes] = useState<string[]>(["synth"]);
  const [busy, setBusy] = useState(false);

  // A key revoked in another tab, or from this console, must not leave the session
  // pretending to be signed in.
  const report = useCallback(
    (cause: unknown) => {
      if (cause instanceof ApiError && cause.status === 401) {
        signOut();
        return;
      }
      setError(cause instanceof ApiError ? cause.message : "Could not reach the gateway.");
    },
    [signOut],
  );

  const load = useCallback(async () => {
    if (!key) return;
    try {
      setKeys(await api.listKeys(key));
    } catch (cause) {
      report(cause);
    }
  }, [key, report]);

  useEffect(() => {
    if (!key) return;
    let cancelled = false;

    void (async () => {
      try {
        const list = await api.listKeys(key);
        if (!cancelled) setKeys(list);
      } catch (cause) {
        if (!cancelled) report(cause);
      }
    })();

    return () => {
      cancelled = true;
    };
  }, [key, report]);

  async function create(event: React.FormEvent) {
    event.preventDefault();
    if (!key || scopes.length === 0) return;
    setBusy(true);
    setError(null);
    try {
      // Held in state, not stored: this is the only moment the secret exists outside
      // the gateway, and it is gone on the next navigation (US-17 AC-1).
      setCreated(await api.createKey(key, name.trim() || "Untitled key", scopes));
      setName("");
      await load();
    } catch (cause) {
      report(cause);
    } finally {
      setBusy(false);
    }
  }

  async function revoke(target: ApiKey) {
    if (!key) return;
    setError(null);
    try {
      await api.revokeKey(key, target.id);
      await load();
    } catch (cause) {
      report(cause);
    }
  }

  // GET /v1/keys returns live keys only, so the list and the count are the same thing.
  const live = keys ?? [];

  return (
    <>
      <header className="section-head">
        <div>
          <p className="eyebrow">API keys</p>
          <p className="figure mono">{live.length}<span className="figure-of"> live</span></p>
        </div>
      </header>

      {created && <RevealedKey created={created} onDismiss={() => setCreated(null)} />}
      {error && <p className="notice notice-error" role="alert">{error}</p>}

      <form className="create" onSubmit={create}>
        <div className="create-row">
          <div className="field">
            <label className="label" htmlFor="name">Name</label>
            <input
              id="name"
              className="input"
              value={name}
              placeholder="Production bot"
              onChange={(event) => setName(event.target.value)}
            />
          </div>
          <button className="button button-primary" type="submit" disabled={busy || scopes.length === 0}>
            {busy ? "Creating…" : "Create key"}
          </button>
        </div>

        <fieldset className="scopes">
          <legend className="label">Scopes</legend>
          {SCOPES.map((scope) => (
            <label key={scope} className="scope mono">
              <input
                type="checkbox"
                checked={scopes.includes(scope)}
                onChange={(event) =>
                  setScopes((current) =>
                    event.target.checked
                      ? [...current, scope]
                      : current.filter((each) => each !== scope),
                  )
                }
              />
              {scope}
            </label>
          ))}
        </fieldset>
      </form>

      {keys === null ? (
        <p className="notice mono">Reading keys…</p>
      ) : keys.length === 0 ? (
        <p className="notice">No keys yet.</p>
      ) : (
        <table className="table">
          <thead>
            <tr>
              <th scope="col">Name</th>
              <th scope="col">Prefix</th>
              <th scope="col">Scopes</th>
              <th scope="col">Created</th>
              <th scope="col"><span className="visually-hidden">Actions</span></th>
            </tr>
          </thead>
          <tbody>
            {keys.map((row) => (
              <tr key={row.id}>
                <td>{row.name}</td>
                <td className="mono">{row.prefix}…</td>
                <td className="mono scope-list">{row.scopes.join(" ")}</td>
                <td className="mono dim">{formatDateTime(row.created_at)}</td>
                <td className="row-action">
                  <button type="button" className="button button-danger" onClick={() => void revoke(row)}>
                    Revoke
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </>
  );
}

/**
 * RevealedKey is the one place a secret appears.
 *
 * It is deliberately loud and deliberately dismissible-once: the gateway stores only a
 * hash, so a tenant who closes this without copying has to create another key.
 */
function RevealedKey({ created, onDismiss }: { created: CreatedKey; onDismiss: () => void }) {
  const [copied, setCopied] = useState(false);

  async function copy() {
    try {
      await navigator.clipboard.writeText(created.secret);
      setCopied(true);
    } catch {
      setCopied(false);
    }
  }

  return (
    <aside className="reveal" role="status">
      <p className="reveal-title">
        Copy <span className="mono">{created.name}</span> now
      </p>
      <p className="reveal-note">
        This is the only time the secret is shown. Only a hash is kept, so it cannot be
        recovered.
      </p>
      <p className="reveal-secret mono">{created.secret}</p>
      <div className="reveal-actions">
        <button type="button" className="button button-primary" onClick={() => void copy()}>
          {copied ? "Copied" : "Copy"}
        </button>
        <button type="button" className="button" onClick={onDismiss}>
          I have it
        </button>
      </div>
    </aside>
  );
}
