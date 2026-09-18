# ADR-006: Deterministic audio cache; cache hits still bill characters

Status: Accepted

## Context
Same input produces the same audio for a given model version. IVR/soundbox tenants
repeat phrases heavily (A4). Pricing must stay simple (F-08, NFR-09).

## Decision
Cache key = `sha256(model_version | voice | normalize_flag | canonical_params |
normalized_text)`. Key is tenant-agnostic. Hits are billed at full character price and
flagged `cached=true` in usage. Text itself is never stored; only the hash.

## Alternatives considered
| Option | Pros | Cons |
|--------|------|------|
| Tenant-scoped cache | Perceived isolation | Loses cross-tenant hits; audio carries no tenant data anyway |
| Free cache hits | Customer-friendly | Revenue unpredictability; encourages gaming |
| Cache by raw text | Simpler | Whitespace variants miss; normalized text is the true input |

## Consequences
Cancellation mid-stream is out of scope here; ADR-010 sets that rule.
Cache is pure margin. Model upgrade invalidates cache by design. Eviction by
`last_hit_at` > 30 days keeps storage bounded (NFR-11).
