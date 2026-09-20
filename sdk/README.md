# SDKs

Thin clients for the ViTTS API. Both are generated where generation helps and hand-written
where it does not: the request and response **types** come from `docs/api/openapi.yaml`
via `make generate`, so they cannot drift from the contract, and the transport is written
by hand because the parts worth wrapping — streaming and bounded retries on overload — are
the parts a generator cannot produce.

| | `sdk/js` | `sdk/python` |
|---|---|---|
| Types from | `openapi-typescript` | `datamodel-code-generator` |
| Transport | `fetch` | `httpx` (sync and async) |
| Streaming | `for await` over chunks | generator, sync and async |
| Retries | 429 and 503, honouring `Retry-After` | the same |

Neither is published to npm or PyPI. That is a release decision, not a build step.

## Install from the repo

```bash
# JavaScript
npm install ./sdk/js

# Python
pip install ./sdk/python
```

## What both clients guarantee

- The key travels in the `Authorization` header, never in a URL, where proxies and browser
  history would keep it.
- A refusal is an exception carrying the API's own `code` (`quota_exceeded`,
  `rate_limited`, …). Branch on the code; the message is prose for a person.
- `429` and `503` are retried, bounded, waiting exactly as long as `Retry-After` says.
  Nothing else is retried — replaying a `422` would just fail again, and replaying a
  stream would repeat audio the caller already has.
- A failure to reach the API at all is a distinct transport error, so "the service said
  no" and "I could not ask" are never confused.
- `createJob` always sends an `Idempotency-Key`, generating one when you do not, because a
  retried create that makes a second job is a bill the customer did not expect.
