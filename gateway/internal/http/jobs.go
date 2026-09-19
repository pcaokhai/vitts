package http

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/pcaokhai/vitts/gateway/internal/auth"
	"github.com/pcaokhai/vitts/gateway/internal/jobs"
)

// HeaderIdempotencyKey is required on job creation (US-14 acceptance criterion 1).
const HeaderIdempotencyKey = "Idempotency-Key"

// maxJobBody is larger than the synthesis limit because a job carries up to 100k
// characters, which is up to 300 KB of UTF-8 Vietnamese.
const maxJobBody = 1 << 20

// JobService is the use case port.
type JobService interface {
	Create(ctx context.Context, req jobs.Request, maxJobChars int32) (jobs.Created, error)
	Get(ctx context.Context, tenantID, jobID uuid.UUID) (jobs.Job, error)
	List(ctx context.Context, tenantID uuid.UUID, before *time.Time, limit int32) ([]jobs.Job, error)
	Cancel(ctx context.Context, tenantID, jobID uuid.UUID) (jobs.Job, error)
	OutputURL(ctx context.Context, job jobs.Job) (string, error)
}

// JobLimits answers the plan's per-job character cap.
type JobLimits interface {
	MaxJobChars(ctx context.Context, planID string) (int32, error)
}

type jobCreateRequest struct {
	Text       string            `json:"text"`
	Voice      string            `json:"voice"`
	Format     string            `json:"format"`
	SampleRate int32             `json:"sample_rate"`
	Normalize  *bool             `json:"normalize"`
	Params     *synthesisParams  `json:"params"`
	WebhookURL string            `json:"webhook_url"`
	Metadata   map[string]string `json:"metadata"`
}

type jobDTO struct {
	ID            string            `json:"id"`
	Status        string            `json:"status"`
	Voice         string            `json:"voice"`
	Format        string            `json:"format"`
	SampleRate    int32             `json:"sample_rate"`
	TotalChars    int32             `json:"total_chars"`
	SegmentsTotal *int32            `json:"segments_total"`
	SegmentsDone  int32             `json:"segments_done"`
	DurationMS    *int32            `json:"duration_ms,omitempty"`
	OutputURL     string            `json:"output_url,omitempty"`
	Error         string            `json:"error,omitempty"`
	Metadata      map[string]string `json:"metadata"`
	CreatedAt     time.Time         `json:"created_at"`
	CompletedAt   *time.Time        `json:"completed_at,omitempty"`
}

// CreateJob handles POST /v1/jobs (US-14, FL-03).
func CreateJob(service JobService, limits JobLimits) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		identity, ok := IdentityFrom(r.Context())
		if !ok {
			WriteProblem(w, r, NewError(CodeUnauthorized, "missing or malformed API key"))
			return
		}

		var body jobCreateRequest
		decoder := json.NewDecoder(io.LimitReader(r.Body, maxJobBody))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&body); err != nil {
			WriteProblem(w, r, NewError(CodeInvalidRequest, "body must match JobCreateRequest"))
			return
		}

		maxJobChars, err := limits.MaxJobChars(r.Context(), identity.PlanID)
		if err != nil {
			WriteProblem(w, r, &Error{Code: CodeInternal, Cause: err})
			return
		}

		normalize := true
		if body.Normalize != nil {
			normalize = *body.Normalize
		}
		voice := body.Voice
		if voice == "" {
			voice = defaultVoice
		}

		created, err := service.Create(r.Context(), jobs.Request{
			TenantID:       identity.TenantID,
			KeyID:          identity.KeyID,
			IdempotencyKey: r.Header.Get(HeaderIdempotencyKey),
			Text:           body.Text,
			VoiceID:        voice,
			Format:         body.Format,
			SampleRate:     body.SampleRate,
			Normalize:      normalize,
			Params:         body.Params.toCacheParams(body.SampleRate),
			WebhookURL:     body.WebhookURL,
			Metadata:       body.Metadata,
		}, maxJobChars)
		if err != nil {
			WriteProblem(w, r, mapJobError(err))
			return
		}

		// 202 either way: the job is accepted and running, and a retry that matched an
		// existing job is the same answer as the first call (US-14 AC-1).
		writeJSON(w, r, http.StatusAccepted, toJobDTO(r.Context(), service, created.Job))
	}
}

// GetJob handles GET /v1/jobs/{jobId}.
func GetJob(service JobService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		identity, jobID, ok := jobRequestContext(w, r)
		if !ok {
			return
		}

		job, err := service.Get(r.Context(), identity.TenantID, jobID)
		if err != nil {
			WriteProblem(w, r, mapJobError(err))
			return
		}

		writeJSON(w, r, http.StatusOK, toJobDTO(r.Context(), service, job))
	}
}

// ListJobs handles GET /v1/jobs.
func ListJobs(service JobService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		identity, ok := IdentityFrom(r.Context())
		if !ok {
			WriteProblem(w, r, NewError(CodeUnauthorized, "missing or malformed API key"))
			return
		}

		var before *time.Time
		if raw := r.URL.Query().Get("before"); raw != "" {
			parsed, err := time.Parse(time.RFC3339, raw)
			if err != nil {
				WriteProblem(w, r, NewError(CodeInvalidRequest, "before must be an RFC 3339 timestamp"))
				return
			}
			before = &parsed
		}

		limit := int32(0)
		if raw := r.URL.Query().Get("limit"); raw != "" {
			parsed, err := strconv.ParseInt(raw, 10, 32)
			if err != nil || parsed < 1 {
				WriteProblem(w, r, NewError(CodeInvalidRequest, "limit must be a positive integer"))
				return
			}
			limit = int32(parsed) // ParseInt with bitSize 32 already bounds this
		}

		found, err := service.List(r.Context(), identity.TenantID, before, limit)
		if err != nil {
			WriteProblem(w, r, mapJobError(err))
			return
		}

		out := make([]jobDTO, 0, len(found))
		for _, job := range found {
			out = append(out, toJobDTO(r.Context(), service, job))
		}
		writeJSON(w, r, http.StatusOK, out)
	}
}

// CancelJob handles DELETE /v1/jobs/{jobId} (US-14 acceptance criterion 5).
func CancelJob(service JobService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		identity, jobID, ok := jobRequestContext(w, r)
		if !ok {
			return
		}

		job, err := service.Cancel(r.Context(), identity.TenantID, jobID)
		if err != nil {
			WriteProblem(w, r, mapJobError(err))
			return
		}

		writeJSON(w, r, http.StatusOK, toJobDTO(r.Context(), service, job))
	}
}

func jobRequestContext(w http.ResponseWriter, r *http.Request) (identity auth.Identity, jobID uuid.UUID, ok bool) {
	id, found := IdentityFrom(r.Context())
	if !found {
		WriteProblem(w, r, NewError(CodeUnauthorized, "missing or malformed API key"))
		return identity, jobID, false
	}

	parsed, err := uuid.Parse(chi.URLParam(r, "jobId"))
	if err != nil {
		// A malformed id cannot name a job of ours, and saying "not found" avoids
		// confirming which ids exist.
		WriteProblem(w, r, NewError(CodeJobNotFound, "no such job"))
		return identity, jobID, false
	}
	return id, parsed, true
}

func toJobDTO(ctx context.Context, service JobService, job jobs.Job) jobDTO {
	dto := jobDTO{
		ID: job.ID.String(), Status: string(job.Status), Voice: job.VoiceID,
		Format: job.Format, SampleRate: job.SampleRate, TotalChars: job.TotalChars,
		SegmentsTotal: job.SegmentsTotal, SegmentsDone: job.SegmentsDone,
		DurationMS: job.DurationMS, Error: job.Error,
		Metadata:  jobs.PublicMetadata(job.Metadata),
		CreatedAt: job.CreatedAt, CompletedAt: job.CompletedAt,
	}

	// A signing failure must not fail the status read: the job's state is the answer the
	// caller asked for, and the link can be fetched again.
	if url, err := service.OutputURL(ctx, job); err == nil {
		dto.OutputURL = url
	}
	return dto
}

func mapJobError(err error) error {
	switch {
	case errors.Is(err, jobs.ErrNotFound):
		return NewError(CodeJobNotFound, "no such job")
	case errors.Is(err, jobs.ErrIdempotencyConflict):
		return NewError(CodeIdempotencyConflict, "this Idempotency-Key was used with a different body")
	case errors.Is(err, jobs.ErrTextTooLong), errors.Is(err, jobs.ErrPlanLimit):
		return NewError(CodeTextTooLong, err.Error())
	case errors.Is(err, jobs.ErrWebhookRejected):
		// 422, not 400: the body parses and is syntactically valid, but names a URL we
		// will not call (US-15 acceptance criterion 3).
		return &Error{
			Code:   CodeInvalidRequest,
			Status: http.StatusUnprocessableEntity,
			Title:  "Webhook URL rejected",
			Detail: err.Error(),
		}
	case errors.Is(err, jobs.ErrInvalidRequest):
		return NewError(CodeInvalidRequest, err.Error())
	default:
		return &Error{Code: CodeInternal, Cause: err}
	}
}
