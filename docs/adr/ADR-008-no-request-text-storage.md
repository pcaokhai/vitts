# ADR-008: Do not persist request text by default

Status: Accepted

## Context
Text may contain PII (names, amounts, OTPs in IVR use). Storing it creates GDPR/PDPD
(Vietnam Decree 13/2023) obligations and breach impact (NFR-07, NFR-11).

## Decision
Persist only `cache_key`, character count and duration. Tenants may opt in per key
(`store_text=true`) for debugging; opted-in text is stored encrypted at rest and purged
after 7 days.

## Consequences
Quality debugging needs tenant cooperation or synthetic corpora. Logs must be scrubbed by
construction (typed log fields, no `%v` of request structs).
