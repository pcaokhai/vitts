# ADR-010: A cancelled stream is metered from delivered audio, never above the character count

Status: Accepted

Extends ADR-006, which sets character-count pricing and does not say what happens when a
caller hangs up mid-stream.

## Context

Streaming callers cancel: an IVR caller hangs up, a bot barges in, a browser tab closes.
Task 0.4 measured this on the shipped voices and the facts are:

- Upstream `synthesize_stream` reports no text position. The worker knows how many audio
  frames it produced and nothing about which characters they correspond to (F-02, ADR-002
  forbids forking model logic to find out).
- Cancellation is checked between frames, so up to one frame of audio is produced after
  the caller stopped listening (US-01 AC-3).
- Speaking rate across four voices and three lengths is 14.6–25.8 characters per second
  of audio (`docs/reports/bench-m0.md`, task 0.4 measurements).

A completed request is unambiguous and is billed the exact character count. Only the
cancelled case needs a rule, and "bill the full text" and "bill nothing" are both wrong:
the first charges for audio the tenant never received, the second makes cancellation a
way to get free synthesis.

## Decision

We will meter a cancelled stream from the audio the gateway actually delivered to the
client, converted at a fixed published rate of **14.5 characters per second**, rounded
down, and capped at the request's character count. The conversion rate is the floor of
the measured range, so the estimate errs towards under-billing. The gateway measures
delivered audio itself from the frames it forwarded — it never trusts a worker-reported
count for billing — and records the row as `status = 'client_cancelled'` with the
delivered `duration_ms`, which is what makes an estimated line identifiable later. The
worker's
`AudioFrame.chars_consumed` stays an operational signal for logs and metrics only.

## Alternatives considered

| Option | Pros | Cons |
|--------|------|------|
| Bill the full character count on cancel | Simplest; matches the cache-hit rule in ADR-006 | Charges for audio never delivered; the tenant can hear the difference and will dispute it |
| Bill nothing on cancel | Customer-friendly | Cancelling just before the last frame becomes free synthesis; the compute was spent either way |
| Bill by audio seconds for everyone | Exactly measurable, no estimate anywhere | Breaks the single published price unit (characters, F-08/NFR-09) and re-prices every existing tenant |
| Derive the text position from the model | Exact | Requires forking model internals, which ADR-002 forbids; upstream exposes no such signal |
| Per-request rate from that request's own audio | No fixed constant | Not knowable until the stream ends, which is exactly the case being metered |

## Consequences

Cancelled requests under-bill slightly by design: a fast voice at 25.8 chars/s metered at
14.5 bills roughly 56% of what it produced. That is the accepted cost of never
over-charging for undelivered audio, and it is bounded because the character count caps it.

We must monitor the share of requests metered as estimated. If it stops being marginal —
say above 5% of billed characters — the rate stops being a rounding detail and this ADR
should be revisited with per-voice rates, which `voices.meta.json` could carry.

The rate is a published number, not a hidden constant: it belongs in the pricing page and
in the API docs next to the cancellation semantics, so a tenant can reconcile their own
invoice. Changing it is a pricing change and needs a new ADR.

No schema change is needed: `synth_requests.status = 'client_cancelled'` already marks
the row as estimated and `duration_ms` already records the delivered audio the estimate
came from, so an estimated line can be found and re-priced later without re-deriving it.
The estimate is applied in the gateway usage meter (task 1.12), not in the worker.
