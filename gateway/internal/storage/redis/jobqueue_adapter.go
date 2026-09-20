package redis

import (
	"context"
	"time"

	"github.com/pcaokhai/vitts/gateway/internal/jobs"
)

// The orchestrator's queue port speaks in its own entry type, so the jobs package does
// not learn about Redis. These methods translate.

// Claim reads new work for this consumer.
func (q *JobQueue) Claim(ctx context.Context, stream, consumer string, count int64, block time.Duration) ([]jobs.QueueEntry, error) {
	entries, err := q.claim(ctx, stream, consumer, count, block)
	if err != nil {
		return nil, err
	}
	return toJobEntries(entries), nil
}

// ClaimStale takes over entries a dead consumer left pending.
func (q *JobQueue) ClaimStale(ctx context.Context, stream, consumer string, idle time.Duration, count int64) ([]jobs.QueueEntry, error) {
	entries, err := q.claimStale(ctx, stream, consumer, idle, count)
	if err != nil {
		return nil, err
	}
	return toJobEntries(entries), nil
}

func toJobEntries(entries []Entry) []jobs.QueueEntry {
	out := make([]jobs.QueueEntry, 0, len(entries))
	for _, entry := range entries {
		out = append(out, jobs.QueueEntry{
			ID: entry.ID, JobID: entry.JobID, Seq: entry.Seq, Stream: entry.Stream,
		})
	}
	return out
}
