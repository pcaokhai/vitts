// Package jobs owns the long-text job lifecycle (US-14, FL-03).
//
// It is the only package that mutates `jobs` and `job_segments`
// (docs/14-engineering-standards.md § Architecture rules, one writer per aggregate).
package jobs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/pcaokhai/vitts/gateway/internal/cache"
)

// Limits from docs/api/openapi.yaml.
const (
	MaxTextChars      = 100_000
	MaxMetadataKeys   = 10
	OutputURLTTL      = 24 * time.Hour
	maxIdempotencyLen = 255
)

// Status is a job's state. The set and its transitions are FL-03.
type Status string

// The job states.
const (
	StatusQueued       Status = "queued"
	StatusSegmenting   Status = "segmenting"
	StatusSynthesizing Status = "synthesizing"
	StatusMerging      Status = "merging"
	StatusCompleted    Status = "completed"
	StatusFailed       Status = "failed"
	StatusCancelled    Status = "cancelled"
)

// Terminal reports whether a job can still change state.
func (s Status) Terminal() bool {
	return s == StatusCompleted || s == StatusFailed || s == StatusCancelled
}

// Errors the transport maps to a status code.
var (
	ErrInvalidRequest      = errors.New("invalid request")
	ErrTextTooLong         = errors.New("text too long")
	ErrIdempotencyConflict = errors.New("idempotency conflict")
	ErrNotFound            = errors.New("job not found")
	ErrWebhookRejected     = errors.New("webhook url rejected")
	ErrPlanLimit           = errors.New("text exceeds the plan's job limit")
)

// Job is one long-text synthesis.
type Job struct {
	ID             uuid.UUID
	TenantID       uuid.UUID
	KeyID          uuid.UUID
	IdempotencyKey string
	Status         Status
	VoiceID        string
	Format         string
	SampleRate     int32
	Normalize      bool
	Params         cache.Params
	TotalChars     int32
	SegmentsTotal  *int32
	SegmentsDone   int32
	OutputS3Key    string
	DurationMS     *int32
	WebhookURL     string
	Metadata       map[string]string
	Error          string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	CompletedAt    *time.Time
}

// Request is a job submission after authentication.
type Request struct {
	TenantID       uuid.UUID
	KeyID          uuid.UUID
	IdempotencyKey string
	Text           string
	VoiceID        string
	Format         string
	SampleRate     int32
	Normalize      bool
	Params         cache.Params
	WebhookURL     string
	Metadata       map[string]string
}

// BodyHash fingerprints everything that makes two submissions the same job.
//
// Idempotency compares this rather than the raw body, so a client that reorders JSON
// fields or reformats whitespace still gets its original job back instead of a 409
// (US-14 acceptance criterion 1).
func (r Request) BodyHash() []byte {
	sum := sha256.New()
	// hash.Hash never returns an error from Write, which is why Fprintf's result is
	// discarded here rather than threaded through a method that cannot fail.
	fields := fmt.Sprintf("%s|%s|%d|%t|%v|%s|%s",
		r.VoiceID, r.Format, r.SampleRate, r.Normalize, r.Params,
		r.WebhookURL, cache.CanonicalText(r.Text))
	sum.Write([]byte(fields))
	return sum.Sum(nil)
}

// BodyFingerprint is the hash in the form stored on the job's metadata.
func (r Request) BodyFingerprint() string { return hex.EncodeToString(r.BodyHash()) }

// Validate applies the contract's bounds and fills defaults.
func (r *Request) Validate(maxJobChars int32) error {
	text := []rune(r.Text)
	switch {
	case len(text) == 0:
		return fmt.Errorf("%w: text must not be empty", ErrInvalidRequest)
	case len(text) > MaxTextChars:
		return fmt.Errorf("%w: text is %d characters, the limit is %d",
			ErrTextTooLong, len(text), MaxTextChars)
	}
	if maxJobChars > 0 && runeCount(text) > maxJobChars {
		return fmt.Errorf("%w: %d characters, this plan allows %d",
			ErrPlanLimit, len(text), maxJobChars)
	}

	if r.IdempotencyKey == "" {
		return fmt.Errorf("%w: Idempotency-Key header is required", ErrInvalidRequest)
	}
	if len(r.IdempotencyKey) > maxIdempotencyLen {
		return fmt.Errorf("%w: Idempotency-Key is too long", ErrInvalidRequest)
	}

	if r.VoiceID == "" {
		return fmt.Errorf("%w: voice is required", ErrInvalidRequest)
	}
	if r.Format == "" {
		r.Format = "mp3" // the contract's default for jobs
	}
	switch r.Format {
	case "mp3", "ogg_opus", "wav":
	default:
		return fmt.Errorf("%w: unknown format %q", ErrInvalidRequest, r.Format)
	}

	if r.SampleRate == 0 {
		r.SampleRate = 48000
	}
	switch r.SampleRate {
	case 8000, 16000, 24000, 48000:
	default:
		return fmt.Errorf("%w: unsupported sample_rate %d", ErrInvalidRequest, r.SampleRate)
	}

	if len(r.Metadata) > MaxMetadataKeys {
		return fmt.Errorf("%w: at most %d metadata keys", ErrInvalidRequest, MaxMetadataKeys)
	}

	return nil
}

// Chars is the job's billable size.
func (r Request) Chars() int32 { return runeCount([]rune(r.Text)) }

// runeCount is the length as an int32, saturating rather than wrapping. MaxTextChars is
// checked before this matters, so saturation is a guard against a future caller that
// forgets to.
func runeCount(text []rune) int32 {
	if len(text) > MaxTextChars {
		return MaxTextChars
	}
	return int32(len(text)) //nolint:gosec // bounded by MaxTextChars on the line above
}

// Repository is the storage port. Every method takes the tenant, because there is no
// read of a tenant-owned row without it (docs/07-permissions.md).
type Repository interface {
	Create(ctx context.Context, job Job) (Job, error)
	ByIdempotencyKey(ctx context.Context, tenantID uuid.UUID, key string) (Job, error)
	Get(ctx context.Context, tenantID, jobID uuid.UUID) (Job, error)
	List(ctx context.Context, tenantID uuid.UUID, before *time.Time, limit int32) ([]Job, error)
	Cancel(ctx context.Context, tenantID, jobID uuid.UUID) (bool, error)
}

// Queue hands a job to the orchestrator.
type Queue interface {
	Enqueue(ctx context.Context, jobID uuid.UUID) error
}

// TextStore holds a job's input text. It never goes in Postgres (ADR-008).
type TextStore interface {
	PutText(ctx context.Context, tenantID, jobID uuid.UUID, text string) error
	SignedOutputURL(ctx context.Context, key string, ttl time.Duration) (string, error)
}

// WebhookGuard vets an outbound URL before we promise to call it.
type WebhookGuard interface {
	Check(ctx context.Context, rawURL string) error
}
