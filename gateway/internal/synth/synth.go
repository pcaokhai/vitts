// Package synth is the synthesis use case: the one place that decides what happens
// between an authenticated request and returned audio (FL-01).
//
// It knows about quota, cache, dispatch and the worker contract, and nothing about HTTP.
package synth

import (
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/pcaokhai/vitts/gateway/internal/cache"
)

// Limits from docs/api/openapi.yaml. They are enforced here as well as at the edge,
// because a use case must not trust its caller.
const (
	MaxTextChars = 3000
	MinTextChars = 1
)

// Format is the container a caller asked for.
type Format string

// The formats the contract offers.
const (
	FormatWAV     Format = "wav"
	FormatMP3     Format = "mp3"
	FormatOggOpus Format = "ogg_opus"
)

// Errors the transport maps to a status code.
var (
	ErrInvalidRequest = errors.New("invalid request")
	ErrTextTooLong    = errors.New("text too long")
	ErrUnknownVoice   = errors.New("unknown voice")
	ErrQuotaExceeded  = errors.New("quota exceeded")
	ErrFormatNotReady = errors.New("format not available yet")
	ErrWorkerFailed   = errors.New("worker failed")
)

// SupportedSampleRates mirrors the OpenAPI enum.
var SupportedSampleRates = []int32{8000, 16000, 24000, 48000}

// Request is one synthesis, after authentication and before any work.
type Request struct {
	TenantID   uuid.UUID
	KeyID      uuid.UUID
	RequestID  string
	Text       string
	VoiceID    string
	Format     Format
	SampleRate int32
	Normalize  bool
	Params     cache.Params
}

// Result is a completed synthesis.
type Result struct {
	Audio        []byte
	DurationMS   int32
	SampleRate   int32
	Format       Format
	CacheHit     bool
	CharsBilled  int64
	VoiceID      string
	ModelVersion string
}

// Validate applies the contract's bounds and fills defaults.
//
// Defaults live here rather than in the handler so every entry point — sync, stream,
// WebSocket and jobs — agrees on what an omitted field means.
func (r *Request) Validate() error {
	text := []rune(r.Text)
	switch {
	case len(text) < MinTextChars:
		return fmt.Errorf("%w: text must not be empty", ErrInvalidRequest)
	case len(text) > MaxTextChars:
		return fmt.Errorf("%w: text is %d characters, the limit is %d",
			ErrTextTooLong, len(text), MaxTextChars)
	}

	if r.VoiceID == "" {
		return fmt.Errorf("%w: voice is required", ErrInvalidRequest)
	}

	if r.Format == "" {
		r.Format = FormatWAV
	}
	switch r.Format {
	case FormatWAV:
	case FormatMP3, FormatOggOpus:
		// Non-PCM containers are produced by the worker's Merge RPC, which lands in
		// task 2.2. Refusing plainly beats returning a WAV mislabelled as MP3.
		return fmt.Errorf("%w: %s arrives with worker Merge (task 2.2)", ErrFormatNotReady, r.Format)
	default:
		return fmt.Errorf("%w: unknown format %q", ErrInvalidRequest, r.Format)
	}

	if r.SampleRate == 0 {
		r.SampleRate = 48000
	}
	if !supportedRate(r.SampleRate) {
		return fmt.Errorf("%w: sample_rate must be one of %v", ErrInvalidRequest, SupportedSampleRates)
	}

	return nil
}

// Chars is what the request costs against quota: characters of text, per ADR-006.
func (r *Request) Chars() int64 { return int64(len([]rune(r.Text))) }

func supportedRate(rate int32) bool {
	for _, supported := range SupportedSampleRates {
		if rate == supported {
			return true
		}
	}
	return false
}
