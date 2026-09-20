# Manual test guide

Every feature in the repo, in the order a person can actually check them, with the exact
command and what a pass looks like. Written to be run top to bottom on a clean machine.

Automated coverage lives in `10-tests.md`; this file is for the things a person verifies
with their own eyes and ears — audio that sounds right, a console that behaves, a refusal
that arrives fast.

Run the files in order. Each is a session you can finish in a sitting, and every
step says what a pass looks like rather than only what to type.

| # | File | What it covers |
|---|------|----------------|
| 1 | [`01-setup.md`](01-setup.md) | Ports, the stack, the plan tiers and a tenant key. Nothing else works until this passes. |
| 2 | [`02-health-and-voices.md`](02-health-and-voices.md) | The two endpoints that answer before any synthesis happens, and the one that needs no key. |
| 3 | [`03-synthesis.md`](03-synthesis.md) | Audio you listen to: sync, streaming, cancellation and the WebSocket. |
| 4 | [`04-limits-and-quota.md`](04-limits-and-quota.md) | Every refusal the API makes, and how fast it makes it. |
| 5 | [`05-cache-and-usage.md`](05-cache-and-usage.md) | What a repeated request costs, and whether the ledger agrees. |
| 6 | [`06-jobs.md`](06-jobs.md) | Long text: segmentation, merge, idempotency and cancel. |
| 7 | [`07-isolation-and-keys.md`](07-isolation-and-keys.md) | The security surface. The isolation check is the most important one in the repo. |
| 8 | [`08-console-and-sdks.md`](08-console-and-sdks.md) | The two things a customer actually touches. |
| 9 | [`09-observability.md`](09-observability.md) | Metrics, dashboards and alerts, checked against a running stack. |
| 10 | [`10-resilience.md`](10-resilience.md) | The runbook drills. Each one found a real bug the first time it ran. |
| 11 | [`11-automated-suites.md`](11-automated-suites.md) | Running CI's own checks by hand. |
| 12 | [`12-gaps-and-teardown.md`](12-gaps-and-teardown.md) | What this guide cannot cover, and how to stop the stack. |

Automated coverage lives in [`../10-tests.md`](../10-tests.md); this folder is for
what a person verifies with their own eyes and ears.
