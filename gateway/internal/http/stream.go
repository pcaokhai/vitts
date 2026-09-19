package http

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/rs/zerolog"

	"github.com/pcaokhai/vitts/gateway/internal/audio"
	"github.com/pcaokhai/vitts/gateway/internal/synth"
)

// Streamer is the use case port.
type Streamer interface {
	Stream(ctx context.Context, req synth.Request, planID string,
		sink synth.StreamSink, leases synth.Leases, streamLimit int32) (synth.StreamResult, error)
}

// StreamLimits answers how many concurrent streams a plan allows.
type StreamLimits interface {
	MaxConcurrentStreams(ctx context.Context, planID string) (int32, error)
}

// SynthesizeStream handles POST /v1/synthesize/stream (US-09).
//
// Audio is written as it is produced, so the response status is committed before the
// synthesis can fail. That is why every refusal the use case can raise happens before the
// first byte: after it, the only honest option left is to truncate and log.
func SynthesizeStream(service Streamer, leases synth.Leases, limits StreamLimits) http.HandlerFunc {
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

		streamLimit, err := limits.MaxConcurrentStreams(r.Context(), identity.PlanID)
		if err != nil {
			WriteProblem(w, r, &Error{Code: CodeInternal, Cause: err})
			return
		}

		req := body.toUseCase(identity.TenantID, identity.KeyID, RequestIDFrom(r.Context()))
		sink := &httpSink{w: w, r: r}

		result, err := service.Stream(r.Context(), req, identity.PlanID, sink, leases, streamLimit)
		if err != nil {
			if sink.started {
				// The body is already on the wire; a problem document would corrupt the
				// audio stream. Truncate and let the log carry the reason.
				zerolog.Ctx(r.Context()).Error().Err(err).Msg("stream aborted after first byte")
				return
			}
			WriteProblem(w, r, mapStreamError(err))
			return
		}

		zerolog.Ctx(r.Context()).Info().
			Int32("duration_ms", result.DurationMS).
			Int32("ttfa_ms", result.TTFAMS).
			Bool("cached", result.CacheHit).
			Bool("cancelled", result.Cancelled).
			Int64("chars_billed", result.CharsBilled).
			Msg("stream complete")
	}
}

// httpSink writes audio to the response, flushing so the client hears frames as they
// arrive rather than when the buffer happens to fill.
type httpSink struct {
	w       http.ResponseWriter
	r       *http.Request
	started bool
}

func (s *httpSink) Header(sampleRate int32) error {
	s.w.Header().Set("Content-Type", "audio/wav")
	s.w.Header().Set(HeaderCache, "MISS")
	// No Content-Length: the length is not known until the synthesis ends, which is what
	// chunked transfer encoding is for.
	s.w.WriteHeader(http.StatusOK)
	s.started = true

	// UnknownSize declares an open-ended RIFF, which players accept and read until the
	// connection closes (docs/api/openapi.yaml).
	if err := audio.WriteHeader(s.w, sampleRate, audio.UnknownSize); err != nil {
		return err
	}
	s.flush()
	return nil
}

func (s *httpSink) Chunk(pcm []byte) error {
	if _, err := s.w.Write(pcm); err != nil {
		return err //nolint:wrapcheck // the caller treats any write failure as a disconnect
	}
	s.flush()
	return nil
}

func (s *httpSink) flush() {
	if err := http.NewResponseController(s.w).Flush(); err != nil {
		// A ResponseWriter that cannot flush would buffer the whole stream, defeating
		// the endpoint; the client will see it as latency and the log records why.
		zerolog.Ctx(s.r.Context()).Warn().Err(err).Msg("response flush unsupported")
	}
}

func mapStreamError(err error) error {
	if errors.Is(err, synth.ErrConcurrencyLimited) {
		return NewError(CodeConcurrencyLimited, "too many concurrent streams for this plan")
	}
	return mapSynthError(err)
}

// StreamCacheHeader is exported for tests that assert the hit/miss marker.
func StreamCacheHeader(result synth.StreamResult) string {
	if result.CacheHit {
		return "HIT"
	}
	return "MISS"
}
