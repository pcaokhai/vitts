"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";

import { maskKey } from "@/lib/session";
import { useKey } from "@/components/KeyProvider";
import { KeyGate } from "@/components/KeyGate";

const NAV = [
  { href: "/", label: "Usage" },
  { href: "/keys", label: "Keys" },
  { href: "/docs", label: "Docs" },
];

// Routes a developer reads before they have a key. Gating these would mean telling
// someone to sign in to find out how to sign in.
const PUBLIC = new Set(["/docs"]);

export function Shell({ children }: { children: React.ReactNode }) {
  const { key, loaded, signOut } = useKey();
  const pathname = usePathname();

  const isPublic = PUBLIC.has(pathname);

  // Until sessionStorage has been read there is nothing true to render: showing the
  // gate first would flash it at every tenant who is already signed in.
  if (!loaded && !isPublic) {
    return <div className="boot" aria-hidden="true" />;
  }
  if (!key && !isPublic) {
    return <KeyGate />;
  }

  return (
    <div className="shell">
      <header className="masthead">
        <div className="masthead-inner">
          <p className="wordmark">
            ViTTS<span className="accent">.</span>
          </p>

          <nav aria-label="Console sections" className="nav">
            {NAV.map((item) => {
              const current = pathname === item.href;
              return (
                <Link
                  key={item.href}
                  href={item.href}
                  className={`nav-link${current ? " is-current" : ""}`}
                  aria-current={current ? "page" : undefined}
                >
                  {item.label}
                </Link>
              );
            })}
          </nav>

          <div className="identity">
            {key ? (
              <>
                <span className="identity-key mono" title="The key this tab is using">
                  {maskKey(key)}
                </span>
                <button type="button" className="button button-quiet" onClick={signOut}>
                  Forget
                </button>
              </>
            ) : (
              <Link href="/" className="button button-quiet">
                Sign in
              </Link>
            )}
          </div>
        </div>
      </header>

      <main className="page">{children}</main>

      <footer className="colophon">
        <p>
          The key is held for this tab only. Closing it signs you out.
        </p>
      </footer>
    </div>
  );
}
