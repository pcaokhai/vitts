"use client";

import { useState } from "react";

import type { Sample } from "@/lib/docs";

/** CodeTabs shows one sample per language, with the copy button a reader reaches for. */
export function CodeTabs({ samples }: { samples: Sample[] }) {
  const [current, setCurrent] = useState(samples[0]?.id ?? "");
  const [copied, setCopied] = useState(false);
  const shown = samples.find((sample) => sample.id === current) ?? samples[0];

  if (!shown) return null;

  async function copy() {
    try {
      await navigator.clipboard.writeText(shown!.code);
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    } catch {
      setCopied(false);
    }
  }

  return (
    <figure className="code">
      <div className="code-bar">
        <div className="range" role="tablist" aria-label="Language">
          {samples.map((sample) => (
            <button
              key={sample.id}
              type="button"
              role="tab"
              aria-selected={sample.id === shown.id}
              className={`range-option${sample.id === shown.id ? " is-current" : ""}`}
              onClick={() => {
                setCurrent(sample.id);
                setCopied(false);
              }}
            >
              {sample.label}
            </button>
          ))}
        </div>
        <button type="button" className="button button-quiet" onClick={() => void copy()}>
          {copied ? "Copied" : "Copy"}
        </button>
      </div>
      <pre className="code-block mono">
        <code>{shown.code}</code>
      </pre>
    </figure>
  );
}
