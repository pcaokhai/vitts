---
paths: ["**"]
---
# Security rules (all files)

- No secrets in code, tests, fixtures, or docs. Use `VITTS_*` env vars; `.env.example` only
  contains placeholders.
- API keys: 32 random bytes, `zt_live_`/`zt_test_` prefix, stored as sha256, compared via
  `subtle.ConstantTimeCompare` after hashing.
- Input bounds are enforced at the edge (OpenAPI validators) and again at the worker.
- Outbound URLs (webhooks) pass the SSRF guard: https only, resolve DNS, reject private,
  loopback, link-local, multicast; re-check on redirect.
- Other tenants' resources return 404. Admin endpoints require `X-Admin-Key` and allowlisted IP.
- Dependencies: pin versions; run `govulncheck`, `pip-audit`, `gitleaks` in CI; base
  images by digest; containers non-root, read-only FS where possible.
- Never disable a security check to make a test pass. If a rule blocks the task, stop and ask.
