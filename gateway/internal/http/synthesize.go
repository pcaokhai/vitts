package http

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/pcaokhai/vitts/gateway/internal/cache"
	"github.com/pcaokhai/vitts/gateway/internal/dispatch"
	"github.com/pcaokhai/vitts/gateway/internal/synth"
)

// maxSynthesizeBody is the 1 MiB limit from FL-01 step 1. It bounds the read before any
// parsing, so an oversized body costs memory proportional to the limit, not to the body.
const maxSynthesizeBody = 1 << 20

// Response headers from US-08 acceptance criterion 1.
const (
	HeaderCache         = "X-Cache"
	HeaderAudioDuration = "X-Audio-Duration-Ms"
	HeaderCharsBilled   = "X-Chars-Billed"
)

type synthesizeRequest struct {
	Text       string           `json:"text"`
	Voice      string           `json:"voice"`
	Format     string           `json:"format"`
	SampleRate int32            `json:"sample_rate"`
	Normalize  *bool            `json:"normalize"`
	Params     *synthesisParams `json:"params"`
}

type synthesisParams struct {
	CFGScale               *float64 `json:"cfg_scale"`
	AudioTemperature       *float64 `json:"audio_temperature"`
	AudioTopK              *int32   `json:"audio_topk"`
	AudioTopP              *float64 `json:"audio_topp"`
	AudioRepetitionPenalty *float64 `json:"audio_repetition_penalty"`
}

// Contract defaults from docs/api/openapi.yaml. They live here as well as in the use
// case because the wire format is what a caller omits.
const (
	defaultVoice                   = "maichi"
	defaultCFGScale                = 1.0
	defaultAudioTemperature        = 0.8
	defaultAudioTopK         int32 = 25
	defaultAudioTopP               = 0.95
	defaultRepetitionPenalty       = 1.2
	defaultEOAExtraFrames    int32 = 1
)

// Synthesizer is the use case port.
type Synthesizer interface {
	Synthesize(ctx context.Context, req synth.Request, planID string) (synth.Result, error)
}

// Synthesize handles POST /v1/synthesize (US-08).
func Synthesize(service Synthesizer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		identity, ok := IdentityFrom(r.Context())
		if !ok {
			WriteProblem(w, r, NewError(CodeUnauthorized, "missing or malformed API key"))
			return
		}

		var body synthesizeRequest
		decoder := json.NewDecoder(io.LimitReader(r.Body, maxSynthesizeBody))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&body); err != nil {
			WriteProblem(w, r, NewError(CodeInvalidRequest, "body must match SynthesizeRequest"))
			return
		}

		req := body.toUseCase(identity.TenantID, identity.KeyID, RequestIDFrom(r.Context()))

		result, err := service.Synthesize(r.Context(), req, identity.PlanID)
		if err != nil {
			WriteProblem(w, r, mapSynthError(err))
			return
		}

		writeAudio(w, r, result)
	}
}

func writeAudio(w http.ResponseWriter, r *http.Request, result synth.Result) {
	cacheState := "MISS"
	if result.CacheHit {
		cacheState = "HIT"
	}

	w.Header().Set("Content-Type", contentType(result.Format))
	w.Header().Set("Content-Length", strconv.Itoa(len(result.Audio)))
	w.Header().Set(HeaderCache, cacheState)
	w.Header().Set(HeaderAudioDuration, strconv.Itoa(int(result.DurationMS)))
	w.Header().Set(HeaderCharsBilled, strconv.FormatInt(result.CharsBilled, 10))

	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(result.Audio); err != nil {
		// The body is already committed; the access log carries the outcome.
		zerolog.Ctx(r.Context()).Warn().Err(err).Msg("audio response not fully written")
	}
}

func contentType(format synth.Format) string {
	switch format {
	case synth.FormatMP3:
		return "audio/mpeg"
	case synth.FormatOggOpus:
		return "audio/ogg"
	case synth.FormatWAV:
		return "audio/wav"
	default:
		return "audio/wav"
	}
}

// mapSynthError is the single place a use-case error becomes a status. Codes come from
// the OpenAPI enum; anything unmapped is `internal` with no detail.
func mapSynthError(err error) error {
	switch {
	case errors.Is(err, synth.ErrTextTooLong):
		return NewError(CodeTextTooLong, err.Error())
	case errors.Is(err, synth.ErrUnknownVoice):
		return NewError(CodeUnknownVoice, "unknown voice")
	case errors.Is(err, synth.ErrInvalidRequest), errors.Is(err, synth.ErrFormatNotReady):
		return NewError(CodeInvalidRequest, err.Error())
	case errors.Is(err, synth.ErrQuotaExceeded):
		return NewError(CodeQuotaExceeded, "monthly character quota exhausted")
	case errors.Is(err, dispatch.ErrOverloaded):
		return overloadProblem(err)
	default:
		return &Error{Code: CodeInternal, Cause: err}
	}
}

// overloadProblem turns a dispatcher refusal into 503 carrying Retry-After.
func overloadProblem(err error) error {
	problem := NewError(CodeOverloaded, "no worker capacity within the latency budget")

	var overload *dispatch.Overload
	if errors.As(err, &overload) {
		problem.RetryAfter = overload.RetryAfter
	}
	return problem
}

func (b synthesizeRequest) toUseCase(tenantID, keyID uuid.UUID, requestID string) synth.Request {
	normalize := true
	if b.Normalize != nil {
		normalize = *b.Normalize
	}

	voice := b.Voice
	if voice == "" {
		voice = defaultVoice
	}

	return synth.Request{
		TenantID:   tenantID,
		KeyID:      keyID,
		RequestID:  requestID,
		Text:       b.Text,
		VoiceID:    voice,
		Format:     synth.Format(b.Format),
		SampleRate: b.SampleRate,
		Normalize:  normalize,
		Params:     b.Params.toCacheParams(b.SampleRate),
	}
}

// toCacheParams fills the contract's defaults for every knob the caller omitted, so two
// requests that differ only in what they left out produce the same cache key.
func (p *synthesisParams) toCacheParams(sampleRate int32) cache.Params {
	params := cache.Params{
		CFGScale:               defaultCFGScale,
		AudioTemperature:       defaultAudioTemperature,
		AudioTopK:              defaultAudioTopK,
		AudioTopP:              defaultAudioTopP,
		AudioRepetitionPenalty: defaultRepetitionPenalty,
		EOAExtraFrames:         defaultEOAExtraFrames,
		SampleRate:             sampleRate,
	}
	if params.SampleRate == 0 {
		params.SampleRate = 48000
	}
	if p == nil {
		return params
	}

	if p.CFGScale != nil {
		params.CFGScale = *p.CFGScale
	}
	if p.AudioTemperature != nil {
		params.AudioTemperature = *p.AudioTemperature
	}
	if p.AudioTopK != nil {
		params.AudioTopK = *p.AudioTopK
	}
	if p.AudioTopP != nil {
		params.AudioTopP = *p.AudioTopP
	}
	if p.AudioRepetitionPenalty != nil {
		params.AudioRepetitionPenalty = *p.AudioRepetitionPenalty
	}
	return params
}
