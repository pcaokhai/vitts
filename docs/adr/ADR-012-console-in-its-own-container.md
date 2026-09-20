# ADR-012: The console is a Next.js app in its own container

Status: Accepted

Required by the "no new infrastructure without an ADR" rule. Implements F-12 and serves
US-13, US-16 and US-17, which already have the APIs it needs.

## Context

The console is the only browser surface the product has: paste a key, see usage, manage
keys. Three options were on the table.

A static bundle embedded in the gateway with `go:embed` is the cheapest to operate —
nothing new to deploy, same origin, no Node at runtime, and it fits the 80 kB microsite
budget in the frontend rules. HTMX with Go templates is cheaper still.

Both couple the console's release cycle to the gateway's. A copy change means rebuilding
and rolling the API. They also put browser-facing assets behind the same process that
serves tenant traffic, so a console bug is deployed the way an API bug is.

This repo is also a demonstration of production engineering, and the console is the part
a reader looks at first.

## Decision

The console is a Next.js application in `console/`, built to its own image and run as its
own service, in compose and in production.

- It is a **pure API client**. It holds no database credential, no admin key, and no
  server-side session store. Every call it makes is one a tenant could make with `curl`.
- The tenant's API key lives in `sessionStorage`, never in a cookie and never on the
  console's server. There is no login endpoint to attack and nothing to leak from the
  console's own storage: the key is the tenant's, scoped, revocable from the console
  itself, and gone when the tab closes.
- The gateway gains a narrow CORS allowance for the console origin
  (`VITTS_CONSOLE_ORIGIN`) on the tenant API only. The admin surface stays same-origin and
  IP-allowlisted.
- The image is built and scanned like the others: pinned base by digest, non-root,
  `npm audit` in the dependency gate beside `govulncheck` and `pip-audit`.

## Consequences

- One more image to build, scan and pin, and Node in the runtime fleet. That is the price
  of the decoupling and it is paid knowingly.
- The console can ship without touching the API, which is the main thing bought here.
- Because the console is only an API client, it can be deleted, replaced or hosted
  statically later without touching the gateway — the CORS variable is the only coupling.
- A browser now holds an API key. This is the standard trade for a keyed developer
  console; it is bounded by scopes, by `sessionStorage` rather than persistent storage,
  and by the tenant's ability to revoke the key from the console itself.
- The 80 kB microsite budget in the frontend rules does not apply: this is an app page,
  budgeted at 300 kB of JS, and the console is expected to land well inside it.
