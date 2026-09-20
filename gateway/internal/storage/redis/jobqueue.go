package redis

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"
	goredis "github.com/redis/go-redis/v9"
)

// Stream names and the consumer group from docs/05-data-model.md § 2.
const (
	StreamJobsPending = "jobs:pending"
	StreamSegments    = "jobs:segments"
	GroupOrchestrator = "orchestrators"
	// maxStreamLen bounds each stream. Entries are claimed within seconds; a backlog
	// this long means the orchestrator is down, and the reconciler re-queues from
	// Postgres rather than from an unbounded stream (FL-03).
	maxStreamLen = 100_000
)

// JobQueue hands work to the orchestrator over Redis Streams (ADR-005).
type JobQueue struct {
	rdb *goredis.Client
}

// NewJobQueue wires the queue.
func NewJobQueue(client *Client) *JobQueue { return &JobQueue{rdb: client.rdb} }

// EnsureGroups creates the consumer groups if they do not exist.
//
// Called at boot by every replica: creating a group twice is harmless, and a missing
// group would make every read fail.
func (q *JobQueue) EnsureGroups(ctx context.Context) error {
	for _, stream := range []string{StreamJobsPending, StreamSegments} {
		err := q.rdb.XGroupCreateMkStream(ctx, stream, GroupOrchestrator, "0").Err()
		if err != nil && !isBusyGroup(err) {
			return fmt.Errorf("create group on %s: %w", stream, err)
		}
	}
	return nil
}

// Enqueue publishes a job id.
func (q *JobQueue) Enqueue(ctx context.Context, jobID uuid.UUID) error {
	return q.add(ctx, StreamJobsPending, map[string]any{"job_id": jobID.String()})
}

// EnqueueSegment publishes one segment of a job.
func (q *JobQueue) EnqueueSegment(ctx context.Context, jobID uuid.UUID, seq int32) error {
	return q.add(ctx, StreamSegments, map[string]any{
		"job_id": jobID.String(),
		"seq":    seq,
	})
}

func (q *JobQueue) add(ctx context.Context, stream string, values map[string]any) error {
	if err := q.rdb.XAdd(ctx, &goredis.XAddArgs{
		Stream: stream,
		MaxLen: maxStreamLen,
		Approx: true, // trimming exactly costs more than it is worth here
		Values: values,
	}).Err(); err != nil {
		return fmt.Errorf("enqueue on %s: %w", stream, err)
	}
	return nil
}

// Entry is one claimed stream entry.
type Entry struct {
	ID     string
	JobID  uuid.UUID
	Seq    int32
	Stream string
}

// Claim reads up to count entries for this consumer, blocking up to block.
func (q *JobQueue) claim(ctx context.Context, stream, consumer string, count int64, block time.Duration) ([]Entry, error) {
	streams, err := q.rdb.XReadGroup(ctx, &goredis.XReadGroupArgs{
		Group:    GroupOrchestrator,
		Consumer: consumer,
		Streams:  []string{stream, ">"},
		Count:    count,
		Block:    block,
	}).Result()
	if err != nil {
		if errors.Is(err, goredis.Nil) {
			return nil, nil // nothing waiting, which is the common case
		}
		return nil, fmt.Errorf("read group on %s: %w", stream, err)
	}

	var entries []Entry
	for _, s := range streams {
		for _, message := range s.Messages {
			entry, ok := toEntry(s.Stream, message)
			if !ok {
				// A malformed entry can never be processed; acknowledge it so it does
				// not sit in the pending list forever.
				_ = q.Ack(ctx, s.Stream, message.ID)
				continue
			}
			entries = append(entries, entry)
		}
	}
	return entries, nil
}

// ClaimStale takes over entries another consumer left pending, which is how a crashed
// orchestrator's work is recovered (FL-03 reconciler).
func (q *JobQueue) claimStale(ctx context.Context, stream, consumer string, idle time.Duration, count int64) ([]Entry, error) {
	messages, _, err := q.rdb.XAutoClaim(ctx, &goredis.XAutoClaimArgs{
		Stream:   stream,
		Group:    GroupOrchestrator,
		Consumer: consumer,
		MinIdle:  idle,
		Start:    "0-0",
		Count:    count,
	}).Result()
	if err != nil {
		return nil, fmt.Errorf("autoclaim on %s: %w", stream, err)
	}

	var entries []Entry
	for _, message := range messages {
		if entry, ok := toEntry(stream, message); ok {
			entries = append(entries, entry)
		}
	}
	return entries, nil
}

// Ack marks an entry handled.
func (q *JobQueue) Ack(ctx context.Context, stream, id string) error {
	if err := q.rdb.XAck(ctx, stream, GroupOrchestrator, id).Err(); err != nil {
		return fmt.Errorf("ack %s on %s: %w", id, stream, err)
	}
	return nil
}

func toEntry(stream string, message goredis.XMessage) (Entry, bool) {
	raw, ok := message.Values["job_id"].(string)
	if !ok {
		return Entry{}, false
	}
	jobID, err := uuid.Parse(raw)
	if err != nil {
		return Entry{}, false
	}

	entry := Entry{ID: message.ID, JobID: jobID, Stream: stream}
	if seq, present := message.Values["seq"]; present {
		if parsed, convErr := toInt32(seq); convErr == nil {
			entry.Seq = parsed
		}
	}
	return entry, true
}

func toInt32(value any) (int32, error) {
	switch v := value.(type) {
	case string:
		var parsed int32
		if _, err := fmt.Sscanf(v, "%d", &parsed); err != nil {
			return 0, fmt.Errorf("parse seq %q: %w", v, err)
		}
		return parsed, nil
	case int64:
		if v > math.MaxInt32 || v < math.MinInt32 {
			return 0, fmt.Errorf("seq %d is out of range", v)
		}
		return int32(v), nil
	default:
		return 0, fmt.Errorf("unexpected seq type %T", value)
	}
}

func isBusyGroup(err error) bool {
	return err != nil && err.Error() == "BUSYGROUP Consumer Group name already exists"
}
