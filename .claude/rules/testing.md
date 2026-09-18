---
paths: ["**/*_test.go", "worker/tests/**", "scripts/loadtest/**"]
---
# Testing rules

- Every task references a `T-xx` in `docs/10-tests.md`; add or update the row and its status.
- Unit tests must not need network, Docker, or model weights.
- Integration tests use testcontainers and the fake worker; they cover the deny path of
  every flow (401/403/404/402/429/503) not just the happy path.
- Cancellation, ordering, and idempotency tests are mandatory for streaming and jobs.
- Load tests live in `scripts/loadtest/*.js` (k6) with thresholds matching NFR-01/02/05;
  results are written to `docs/reports/`.
- Flaky tests are fixed or deleted, never retried in CI.
