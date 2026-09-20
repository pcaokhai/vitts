package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/pcaokhai/vitts/gateway/internal/cache"
	"github.com/pcaokhai/vitts/gateway/internal/jobs"
	"github.com/pcaokhai/vitts/gateway/internal/storage/postgres/db"
)

// JobRepository implements jobs.Repository.
type JobRepository struct {
	pool *Pool
}

// NewJobRepository wires the repository.
func NewJobRepository(pool *Pool) *JobRepository { return &JobRepository{pool: pool} }

// Create inserts a job row.
func (r *JobRepository) Create(ctx context.Context, job jobs.Job) (jobs.Job, error) {
	params, err := json.Marshal(job.Params)
	if err != nil {
		return jobs.Job{}, fmt.Errorf("encode params: %w", err)
	}
	metadata, err := json.Marshal(job.Metadata)
	if err != nil {
		return jobs.Job{}, fmt.Errorf("encode metadata: %w", err)
	}

	row, err := r.pool.Queries.CreateJob(ctx, db.CreateJobParams{
		ID:             job.ID,
		TenantID:       job.TenantID,
		ApiKeyID:       job.KeyID,
		IdempotencyKey: job.IdempotencyKey,
		VoiceID:        job.VoiceID,
		Format:         job.Format,
		SampleRate:     job.SampleRate,
		Normalize:      job.Normalize,
		Params:         params,
		TotalChars:     job.TotalChars,
		WebhookUrl:     nullableString(job.WebhookURL),
		Metadata:       metadata,
	})
	if err != nil {
		return jobs.Job{}, fmt.Errorf("insert job: %w", err)
	}
	return toJob(row)
}

// ByIdempotencyKey finds a tenant's earlier submission.
func (r *JobRepository) ByIdempotencyKey(ctx context.Context, tenantID uuid.UUID, key string) (jobs.Job, error) {
	row, err := r.pool.Queries.GetJobByIdempotencyKey(ctx, db.GetJobByIdempotencyKeyParams{
		TenantID: tenantID, IdempotencyKey: key,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return jobs.Job{}, jobs.ErrNotFound
		}
		return jobs.Job{}, fmt.Errorf("get job by idempotency key: %w", err)
	}
	return toJob(row)
}

// Get returns one job. Another tenant's job is ErrNotFound, never a permission error
// (docs/07-permissions.md).
func (r *JobRepository) Get(ctx context.Context, tenantID, jobID uuid.UUID) (jobs.Job, error) {
	row, err := r.pool.Queries.GetJob(ctx, db.GetJobParams{ID: jobID, TenantID: tenantID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return jobs.Job{}, jobs.ErrNotFound
		}
		return jobs.Job{}, fmt.Errorf("get job: %w", err)
	}
	return toJob(row)
}

// List returns a page of the tenant's jobs.
func (r *JobRepository) List(ctx context.Context, tenantID uuid.UUID, before *time.Time, limit int32) ([]jobs.Job, error) {
	cursor := pgtype.Timestamptz{}
	if before != nil {
		cursor = pgtype.Timestamptz{Time: *before, Valid: true}
	}

	rows, err := r.pool.Queries.ListJobs(ctx, db.ListJobsParams{
		TenantID: tenantID, Before: cursor, Limit: limit,
	})
	if err != nil {
		return nil, fmt.Errorf("list jobs: %w", err)
	}

	out := make([]jobs.Job, 0, len(rows))
	for _, row := range rows {
		job, convErr := toJob(row)
		if convErr != nil {
			return nil, convErr
		}
		out = append(out, job)
	}
	return out, nil
}

// Cancel marks a non-terminal job cancelled. The boolean reports whether it changed.
func (r *JobRepository) Cancel(ctx context.Context, tenantID, jobID uuid.UUID) (bool, error) {
	affected, err := r.pool.Queries.CancelJob(ctx, db.CancelJobParams{ID: jobID, TenantID: tenantID})
	if err != nil {
		return false, fmt.Errorf("cancel job: %w", err)
	}
	return affected > 0, nil
}

func toJob(row db.Job) (jobs.Job, error) {
	var params cache.Params
	if len(row.Params) > 0 {
		if err := json.Unmarshal(row.Params, &params); err != nil {
			return jobs.Job{}, fmt.Errorf("decode params: %w", err)
		}
	}

	metadata := map[string]string{}
	if len(row.Metadata) > 0 {
		if err := json.Unmarshal(row.Metadata, &metadata); err != nil {
			return jobs.Job{}, fmt.Errorf("decode metadata: %w", err)
		}
	}

	job := jobs.Job{
		ID: row.ID, TenantID: row.TenantID, KeyID: row.ApiKeyID,
		IdempotencyKey: row.IdempotencyKey, Status: jobs.Status(row.Status),
		VoiceID: row.VoiceID, Format: row.Format, SampleRate: row.SampleRate,
		Normalize: row.Normalize, Params: params, TotalChars: row.TotalChars,
		SegmentsTotal: row.SegmentsTotal, SegmentsDone: row.SegmentsDone,
		DurationMS: row.DurationMs, Metadata: metadata,
		CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time,
	}
	if row.OutputS3Key != nil {
		job.OutputS3Key = *row.OutputS3Key
	}
	if row.WebhookUrl != nil {
		job.WebhookURL = *row.WebhookUrl
	}
	if row.Error != nil {
		job.Error = *row.Error
	}
	if row.CompletedAt.Valid {
		completed := row.CompletedAt.Time
		job.CompletedAt = &completed
	}
	return job, nil
}

// --- segment and state-machine operations ---------------------------------------
//
// These belong to the orchestrator's half of the aggregate. Every transition is a
// conditional update: zero rows affected means someone else moved the job, and the
// caller re-reads rather than overwriting (docs/05-data-model.md § 4).

// Transition moves a job from one status to another, reporting whether it won the race.
func (r *JobRepository) Transition(ctx context.Context, jobID uuid.UUID, from, to jobs.Status) (bool, error) {
	affected, err := r.pool.Queries.TransitionJob(ctx, db.TransitionJobParams{
		ID: jobID, Status: string(from), Status_2: string(to),
	})
	if err != nil {
		return false, fmt.Errorf("transition job: %w", err)
	}
	return affected > 0, nil
}

// SetSegmentsTotal records the segment count and moves the job to synthesizing.
func (r *JobRepository) SetSegmentsTotal(ctx context.Context, jobID uuid.UUID, total int32) (bool, error) {
	affected, err := r.pool.Queries.SetJobSegmentsTotal(ctx, db.SetJobSegmentsTotalParams{
		ID: jobID, SegmentsTotal: &total,
	})
	if err != nil {
		return false, fmt.Errorf("set segments total: %w", err)
	}
	return affected > 0, nil
}

// CreateSegment records one planned segment. Repeating a create is harmless, which is
// what lets segmentation be retried after a crash (T-09).
func (r *JobRepository) CreateSegment(ctx context.Context, jobID uuid.UUID, seq int32, textHash []byte, chars int32) error {
	if err := r.pool.Queries.CreateJobSegment(ctx, db.CreateJobSegmentParams{
		ID: uuid.New(), JobID: jobID, Seq: seq, TextHash: textHash, Chars: chars,
	}); err != nil {
		return fmt.Errorf("create segment: %w", err)
	}
	return nil
}

// Segments lists a job's segments in order.
func (r *JobRepository) Segments(ctx context.Context, jobID uuid.UUID) ([]jobs.Segment, error) {
	rows, err := r.pool.Queries.ListJobSegments(ctx, jobID)
	if err != nil {
		return nil, fmt.Errorf("list segments: %w", err)
	}

	out := make([]jobs.Segment, 0, len(rows))
	for _, row := range rows {
		segment := jobs.Segment{
			JobID: row.JobID, Seq: row.Seq, TextHash: row.TextHash,
			Chars: row.Chars, Status: jobs.SegmentStatus(row.Status), Attempts: row.Attempts,
		}
		if row.S3Key != nil {
			segment.S3Key = *row.S3Key
		}
		if row.LastError != nil {
			segment.LastError = *row.LastError
		}
		out = append(out, segment)
	}
	return out, nil
}

// ClaimSegment marks a segment running. False means another consumer has it, or it is
// already done - which is how a replayed stream entry stops short of re-synthesizing.
func (r *JobRepository) ClaimSegment(ctx context.Context, jobID uuid.UUID, seq int32) (bool, error) {
	affected, err := r.pool.Queries.ClaimJobSegment(ctx, db.ClaimJobSegmentParams{JobID: jobID, Seq: seq})
	if err != nil {
		return false, fmt.Errorf("claim segment: %w", err)
	}
	return affected > 0, nil
}

// CompleteSegment marks a segment done and increments the job's counter in one
// statement, so the two can never disagree (docs/05-data-model.md § 4).
func (r *JobRepository) CompleteSegment(ctx context.Context, jobID uuid.UUID, seq int32, s3Key string) error {
	if _, err := r.pool.Queries.CompleteJobSegment(ctx, db.CompleteJobSegmentParams{
		JobID: jobID, Seq: seq, S3Key: &s3Key,
	}); err != nil {
		return fmt.Errorf("complete segment: %w", err)
	}
	return nil
}

// FailSegment records why a segment could not be produced.
func (r *JobRepository) FailSegment(ctx context.Context, jobID uuid.UUID, seq int32, reason string) error {
	if _, err := r.pool.Queries.FailJobSegment(ctx, db.FailJobSegmentParams{
		JobID: jobID, Seq: seq, LastError: &reason,
	}); err != nil {
		return fmt.Errorf("fail segment: %w", err)
	}
	return nil
}

// UnfinishedSegments counts segments that are not done.
func (r *JobRepository) UnfinishedSegments(ctx context.Context, jobID uuid.UUID) (int64, error) {
	count, err := r.pool.Queries.CountUnfinishedSegments(ctx, jobID)
	if err != nil {
		return 0, fmt.Errorf("count unfinished segments: %w", err)
	}
	return count, nil
}

// Fail marks a job failed with a reason, unless it already reached a terminal state.
func (r *JobRepository) Fail(ctx context.Context, jobID uuid.UUID, reason string) error {
	if _, err := r.pool.Queries.FailJob(ctx, db.FailJobParams{ID: jobID, Error: &reason}); err != nil {
		return fmt.Errorf("fail job: %w", err)
	}
	return nil
}

// Complete records the merged output.
func (r *JobRepository) Complete(ctx context.Context, jobID uuid.UUID, outputKey string, durationMS int32) (bool, error) {
	affected, err := r.pool.Queries.CompleteJob(ctx, db.CompleteJobParams{
		ID: jobID, OutputS3Key: &outputKey, DurationMs: &durationMS,
	})
	if err != nil {
		return false, fmt.Errorf("complete job: %w", err)
	}
	return affected > 0, nil
}

// Stuck lists live jobs that have not changed since `before`, which is how the
// reconciler finds work a crashed orchestrator abandoned (FL-03).
func (r *JobRepository) Stuck(ctx context.Context, before time.Time, limit int32) ([]jobs.Job, error) {
	rows, err := r.pool.Queries.StuckJobs(ctx, db.StuckJobsParams{
		UpdatedAt: pgtype.Timestamptz{Time: before, Valid: true}, Limit: limit,
	})
	if err != nil {
		return nil, fmt.Errorf("list stuck jobs: %w", err)
	}

	out := make([]jobs.Job, 0, len(rows))
	for _, row := range rows {
		job, convErr := toJob(row)
		if convErr != nil {
			return nil, convErr
		}
		out = append(out, job)
	}
	return out, nil
}

// ByID loads a job without a tenant filter.
//
// Only the orchestrator uses it: it works from a queue entry that carries no tenant, and
// every tenant-facing read goes through Get, which does filter (docs/07-permissions.md).
func (r *JobRepository) ByID(ctx context.Context, jobID uuid.UUID) (jobs.Job, error) {
	row, err := r.pool.Queries.GetJobByID(ctx, jobID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return jobs.Job{}, jobs.ErrNotFound
		}
		return jobs.Job{}, fmt.Errorf("get job by id: %w", err)
	}
	return toJob(row)
}
