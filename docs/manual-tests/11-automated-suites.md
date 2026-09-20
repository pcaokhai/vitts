# 11. Automated suites

Running CI's own checks by hand.

## The automated suites, by hand

```bash
make lint              # Go, Python, TypeScript, CSS, gitleaks, buf
make test              # unit: Go, Python, console, both SDKs
make test-integration  # testcontainers: Postgres, Redis, MinIO, fake worker
make generate && git diff --exit-code   # contract drift, including SDK types
make audit             # govulncheck, pip-audit, npm audit
make smoke             # health, sync, stream, job end to end
make bench             # worker RTF and TTFA on this machine
```

**Pass:** all green, and `make generate` leaves the tree clean.

Load tests need a key and a scenario:

```bash
VITTS_KEY="$KEY" VITTS_SCENARIO=steady make loadtest
```

**Pass:** the thresholds in `scripts/loadtest/` hold; a 429 or 503 is a *measurement*, not
a failure, and its latency should be in the milliseconds.

---

---

[← Resilience drills](10-resilience.md) · [Index](README.md) · [Gaps and teardown →](12-gaps-and-teardown.md)
