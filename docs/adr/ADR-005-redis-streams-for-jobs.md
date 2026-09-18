# ADR-005: Redis Streams for the job queue

Status: Accepted

## Context
Batch jobs are low volume (hundreds per minute at most) but must not be lost and must be
retried per segment (F-03, F-10). Redis is already required for rate limiting and cache
index.

## Decision
We will use Redis Streams with a consumer group for job dispatch, with Postgres as the
system of record (job row written first; stream entry second; a reconciler re-enqueues
`queued` jobs missing from the stream — outbox pattern without a separate table).

## Alternatives considered
| Option | Pros | Cons |
|--------|------|------|
| Kafka | Durable, replayable | Operational weight far above MVP needs |
| NATS JetStream | Light, durable | One more system to run |
| Postgres `SKIP LOCKED` queue | No new infra | Polling load; Redis already present |

## Consequences
Queue interface is abstracted (`jobs.Queue`) so Kafka can replace it if volume demands.
Redis must run with AOF persistence. Pending-entry list (PEL) must be reclaimed by a
janitor for crashed consumers.
