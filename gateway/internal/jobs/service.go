package jobs

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

// fingerprintKey is where the body hash lives on the job's metadata, so a repeat
// submission can be compared without storing the text (ADR-008).
const fingerprintKey = "_body_sha256"

// DefaultListLimit bounds a page of jobs; pagination is by cursor (14-engineering-standards).
const DefaultListLimit int32 = 50

// MaxListLimit is the largest page a caller may ask for.
const MaxListLimit int32 = 200

// Service creates and reads jobs.
type Service struct {
	repo   Repository
	queue  Queue
	texts  TextStore
	guard  WebhookGuard
	logger zerolog.Logger
}

// NewService wires the use case.
func NewService(repo Repository, queue Queue, texts TextStore, guard WebhookGuard, logger zerolog.Logger) *Service {
	return &Service{repo: repo, queue: queue, texts: texts, guard: guard, logger: logger}
}

// Created reports whether a submission produced a new job or matched an existing one.
type Created struct {
	Job     Job
	Existed bool
}

// Create submits a job (FL-03 steps 1-4).
//
// The idempotency key is checked first and compared by body fingerprint: a retry after a
// timeout must return the original job, while the same key with a different body is a
// client bug worth reporting rather than silently serving the wrong audio.
func (s *Service) Create(ctx context.Context, req Request, maxJobChars int32) (Created, error) {
	if err := req.Validate(maxJobChars); err != nil {
		return Created{}, err
	}
	if err := s.guard.Check(ctx, req.WebhookURL); err != nil {
		return Created{}, err
	}

	fingerprint := req.BodyFingerprint()

	existing, err := s.repo.ByIdempotencyKey(ctx, req.TenantID, req.IdempotencyKey)
	switch {
	case err == nil:
		if existing.Metadata[fingerprintKey] != fingerprint {
			return Created{}, fmt.Errorf("%w: this key was used with a different body",
				ErrIdempotencyConflict)
		}
		return Created{Job: existing, Existed: true}, nil
	case !errors.Is(err, ErrNotFound):
		return Created{}, fmt.Errorf("look up idempotency key: %w", err)
	}

	metadata := make(map[string]string, len(req.Metadata)+1)
	for k, v := range req.Metadata {
		metadata[k] = v
	}
	metadata[fingerprintKey] = fingerprint

	job := Job{
		ID: uuid.New(), TenantID: req.TenantID, KeyID: req.KeyID,
		IdempotencyKey: req.IdempotencyKey, Status: StatusQueued,
		VoiceID: req.VoiceID, Format: req.Format, SampleRate: req.SampleRate,
		Normalize: req.Normalize, Params: req.Params, TotalChars: req.Chars(),
		WebhookURL: req.WebhookURL, Metadata: metadata,
	}

	// Text goes to object storage before the row exists: a job row pointing at text we
	// failed to store would be unrunnable, while orphaned text is swept by lifecycle
	// rules (docs/05-data-model.md § 3).
	if err := s.texts.PutText(ctx, job.TenantID, job.ID, req.Text); err != nil {
		return Created{}, fmt.Errorf("store job text: %w", err)
	}

	created, err := s.repo.Create(ctx, job)
	if err != nil {
		// A concurrent submission with the same key won the unique index. Return its
		// job rather than an error: both callers asked for the same work.
		if raced, lookupErr := s.repo.ByIdempotencyKey(ctx, req.TenantID, req.IdempotencyKey); lookupErr == nil {
			if raced.Metadata[fingerprintKey] != fingerprint {
				return Created{}, fmt.Errorf("%w: this key was used with a different body",
					ErrIdempotencyConflict)
			}
			return Created{Job: raced, Existed: true}, nil
		}
		return Created{}, fmt.Errorf("create job: %w", err)
	}

	// Enqueue last: a queued id that has no row would make the orchestrator chase a
	// ghost, while a row nothing queued is picked up by the reconciler (FL-03).
	if err := s.queue.Enqueue(ctx, created.ID); err != nil {
		s.logger.Error().Err(err).Str("job_id", created.ID.String()).
			Msg("job created but not queued; the reconciler will pick it up")
	}

	return Created{Job: created}, nil
}

// Get returns one job belonging to the tenant.
func (s *Service) Get(ctx context.Context, tenantID, jobID uuid.UUID) (Job, error) {
	job, err := s.repo.Get(ctx, tenantID, jobID)
	if err != nil {
		return Job{}, err
	}
	return job, nil
}

// List returns a page of the tenant's jobs, newest first.
func (s *Service) List(ctx context.Context, tenantID uuid.UUID, before *time.Time, limit int32) ([]Job, error) {
	switch {
	case limit <= 0:
		limit = DefaultListLimit
	case limit > MaxListLimit:
		limit = MaxListLimit
	}

	found, err := s.repo.List(ctx, tenantID, before, limit)
	if err != nil {
		return nil, fmt.Errorf("list jobs: %w", err)
	}
	return found, nil
}

// Cancel stops a job that has not finished (US-14 acceptance criterion 5).
//
// Cancelling an already-terminal job is not an error: a client retrying a cancel it is
// unsure about should not get a failure for succeeding twice.
func (s *Service) Cancel(ctx context.Context, tenantID, jobID uuid.UUID) (Job, error) {
	if _, err := s.repo.Cancel(ctx, tenantID, jobID); err != nil {
		return Job{}, fmt.Errorf("cancel job: %w", err)
	}

	job, err := s.repo.Get(ctx, tenantID, jobID)
	if err != nil {
		return Job{}, err
	}
	return job, nil
}

// OutputURL signs a download link for a completed job (US-14 acceptance criterion 4).
func (s *Service) OutputURL(ctx context.Context, job Job) (string, error) {
	if job.Status != StatusCompleted || job.OutputS3Key == "" {
		return "", nil
	}

	url, err := s.texts.SignedOutputURL(ctx, job.OutputS3Key, OutputURLTTL)
	if err != nil {
		return "", fmt.Errorf("sign output url: %w", err)
	}
	return url, nil
}

// PublicMetadata strips internal bookkeeping before a job is shown to its tenant.
func PublicMetadata(metadata map[string]string) map[string]string {
	out := make(map[string]string, len(metadata))
	for k, v := range metadata {
		if k == fingerprintKey {
			continue
		}
		out[k] = v
	}
	return out
}
