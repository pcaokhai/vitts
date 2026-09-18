// Package http adapts HTTP to the gateway's use cases. Handlers parse, validate, call a
// use case and write a response; business rules live behind the use case boundary.
package http

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/rs/zerolog"
)

// ContentTypeProblem is the media type every error response uses (RFC 9457).
const ContentTypeProblem = "application/problem+json"

// Code is a stable machine-readable error code. The set is the `Problem.code` enum in
// docs/api/openapi.yaml; adding one there comes first.
type Code string

// The complete Problem.code enum. Every error the API can return uses one of these.
const (
	CodeInvalidRequest      Code = "invalid_request"
	CodeTextTooLong         Code = "text_too_long"
	CodeUnknownVoice        Code = "unknown_voice"
	CodeUnauthorized        Code = "unauthorized"
	CodeForbiddenScope      Code = "forbidden_scope"
	CodeQuotaExceeded       Code = "quota_exceeded"
	CodeRateLimited         Code = "rate_limited"
	CodeConcurrencyLimited  Code = "concurrency_limited"
	CodeOverloaded          Code = "overloaded"
	CodeJobNotFound         Code = "job_not_found"
	CodeIdempotencyConflict Code = "idempotency_conflict"
	CodeInternal            Code = "internal"
)

// problemTypeBase namespaces the `type` URI. Each code documents itself at this prefix.
const problemTypeBase = "https://docs.vitts.dev/errors/"

// Problem is the wire shape of an error, matching the OpenAPI schema.
type Problem struct {
	Type      string `json:"type"`
	Title     string `json:"title"`
	Status    int    `json:"status"`
	Detail    string `json:"detail,omitempty"`
	Instance  string `json:"instance,omitempty"`
	Code      Code   `json:"code"`
	RequestID string `json:"request_id,omitempty"`
}

// Error carries everything needed to render a problem response. Use cases return domain
// errors; the mapping to HTTP happens here and nowhere else.
type Error struct {
	Code   Code
	Status int
	Title  string
	Detail string
	// Cause is logged, never serialised: it may embed internal detail.
	Cause error
}

func (e *Error) Error() string {
	if e.Cause != nil {
		return string(e.Code) + ": " + e.Cause.Error()
	}
	return string(e.Code)
}

func (e *Error) Unwrap() error { return e.Cause }

// NewError builds an Error with the status and title registered for the code.
func NewError(code Code, detail string) *Error {
	status, title := statusFor(code)
	return &Error{Code: code, Status: status, Title: title, Detail: detail}
}

// WriteProblem renders err as problem+json. This is the only place that writes an error
// response, so every error the API can return carries a code from the enum.
//
// An error that is not an *Error is reported as `internal` with no detail: an unmapped
// error's message is not something we promise to a caller.
func WriteProblem(w http.ResponseWriter, r *http.Request, err error) {
	problem := toProblem(r, err)

	logProblem(r, problem, err)

	w.Header().Set("Content-Type", ContentTypeProblem)
	w.WriteHeader(problem.Status)
	if encodeErr := json.NewEncoder(w).Encode(problem); encodeErr != nil {
		zerolog.Ctx(r.Context()).Warn().Err(encodeErr).Msg("problem response not written")
	}
}

func toProblem(r *http.Request, err error) Problem {
	var mapped *Error
	if !errors.As(err, &mapped) {
		mapped = NewError(CodeInternal, "")
	}
	if mapped.Status == 0 || mapped.Title == "" {
		status, title := statusFor(mapped.Code)
		if mapped.Status == 0 {
			mapped.Status = status
		}
		if mapped.Title == "" {
			mapped.Title = title
		}
	}

	return Problem{
		Type:      problemTypeBase + string(mapped.Code),
		Title:     mapped.Title,
		Status:    mapped.Status,
		Detail:    mapped.Detail,
		Instance:  r.URL.Path,
		Code:      mapped.Code,
		RequestID: RequestIDFrom(r.Context()),
	}
}

func logProblem(r *http.Request, problem Problem, err error) {
	event := zerolog.Ctx(r.Context()).Warn()
	if problem.Status >= http.StatusInternalServerError {
		event = zerolog.Ctx(r.Context()).Error()
	}
	event.Str("code", string(problem.Code)).
		Int("status", problem.Status).
		Err(err).
		Msg("request failed")
}

// statusFor is the single code → (status, title) table. Keep it exhaustive: an unlisted
// code would silently become a 500.
func statusFor(code Code) (int, string) {
	switch code {
	case CodeInvalidRequest:
		return http.StatusBadRequest, "Invalid request"
	case CodeTextTooLong:
		return http.StatusBadRequest, "Text too long"
	case CodeUnknownVoice:
		return http.StatusBadRequest, "Unknown voice"
	case CodeUnauthorized:
		return http.StatusUnauthorized, "Unauthorized"
	case CodeForbiddenScope:
		return http.StatusForbidden, "Forbidden scope"
	case CodeJobNotFound:
		return http.StatusNotFound, "Job not found"
	case CodeIdempotencyConflict:
		return http.StatusConflict, "Idempotency conflict"
	case CodeQuotaExceeded:
		return http.StatusTooManyRequests, "Quota exceeded"
	case CodeRateLimited:
		return http.StatusTooManyRequests, "Rate limited"
	case CodeConcurrencyLimited:
		return http.StatusTooManyRequests, "Concurrency limited"
	case CodeOverloaded:
		return http.StatusServiceUnavailable, "Overloaded"
	case CodeInternal:
		return http.StatusInternalServerError, "Internal error"
	default:
		return http.StatusInternalServerError, "Internal error"
	}
}
