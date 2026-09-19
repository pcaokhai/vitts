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
