package synth

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"time"

	"github.com/google/uuid"

	"github.com/pcaokhai/vitts/gateway/internal/audio"
	"github.com/pcaokhai/vitts/gateway/internal/cache"
	"github.com/pcaokhai/vitts/gateway/internal/dispatch"
	workerpb "github.com/pcaokhai/vitts/gateway/internal/gen/workerpb"
	"github.com/pcaokhai/vitts/gateway/internal/voices"
)

// IdleTimeout closes a stream whose worker has gone quiet (US-09 acceptance criterion 4).
// A stalled worker holding a client connection open is worse than a truncated response.
const IdleTimeout = 30 * time.Second

// Lease is a held concurrency slot. The stream renews it while it runs and releases it at
// the end, so a crashed gateway frees the tenant's capacity within one TTL.
type Lease interface {
	Renew(ctx context.Context) error
	Release(ctx context.Context)
}

// Leases hands out per-tenant concurrency slots.
type Leases interface {
	// Acquire returns ok=false when the tenant is at max_concurrent_streams, which the
	// transport turns into 429 concurrency_limited (US-06).
	Acquire(ctx context.Context, tenantID uuid.UUID, limit int32) (Lease, bool, error)
}

// ErrConcurrencyLimited means the tenant already holds its maximum streams.
var ErrConcurrencyLimited = errors.New("concurrency limited")

// StreamSink receives audio as it is produced.
type StreamSink interface {
	// Header is called once, before any audio, with the sample rate.
	Header(sampleRate int32) error
	// Chunk is called per frame. Returning an error stops the stream, which is how a
	// disconnected client cancels inference.
	Chunk(pcm []byte) error
}

// StreamResult describes a finished stream.
type StreamResult struct {
	DurationMS  int32
	TTFAMS      int32
	CacheHit    bool
	CharsBilled int64
	Cancelled   bool
}

// Stream synthesizes and delivers audio as it is produced (US-09, FL-01).
//
// The client's context is the cancellation signal all the way down to the worker: when
// it goes, the gRPC call is cancelled, the partial audio is discarded rather than cached,
// and the tenant is billed for what was delivered (ADR-010).
func (s *Service) Stream(
	ctx context.Context, req Request, planID string, sink StreamSink, leases Leases, streamLimit int32,
) (StreamResult, error) {
	if err := req.Validate(); err != nil {
		return StreamResult{}, err
	}

	voice, err := s.resolveVoice(ctx, req)
	if err != nil {
		return StreamResult{}, err
	}

	if err := s.checkQuota(ctx, req, planID); err != nil {
		return StreamResult{}, err
	}

	lease, ok, err := leases.Acquire(ctx, req.TenantID, streamLimit)
	if err != nil {
		return StreamResult{}, fmt.Errorf("acquire stream lease: %w", err)
	}
	if !ok {
		return StreamResult{}, fmt.Errorf("%w: %d concurrent streams", ErrConcurrencyLimited, streamLimit)
	}
	defer lease.Release(context.WithoutCancel(ctx))

	key := cache.Derive(cache.Input{
		ModelVersion: voice.ModelVersion, VoiceID: voice.ID,
		Normalize: req.Normalize, Params: req.Params, Text: req.Text,
	})

	if result, ok := s.streamFromCache(ctx, req, key, sink); ok {
		status := "ok"
		if result.Cancelled {
			status = "client_cancelled"
		}
		s.meterStream(context.WithoutCancel(ctx), req, key, voice, result, status)
		return result, nil
	}

	return s.streamFromWorker(ctx, req, key, voice, sink, lease)
}

// streamFromCache replays a cached object. It is still a stream to the client: the same
// framing, just sourced from storage.
func (s *Service) streamFromCache(ctx context.Context, req Request, key cache.Key, sink StreamSink) (StreamResult, bool) {
	entry, body, err := s.cache.Lookup(ctx, key)
	if err != nil {
		if !errors.Is(err, cache.ErrMiss) {
			s.logger.Warn().Err(err).Str("request_id", req.RequestID).Msg("cache lookup failed")
		}
		return StreamResult{}, false
	}
	defer func() { _ = body.Close() }()

	if err := sink.Header(req.SampleRate); err != nil {
		return StreamResult{}, false
	}

	started := time.Now()
	buf := make([]byte, 32<<10)
	var written int
	for {
		n, readErr := body.Read(buf)
		if n > 0 {
			if err := sink.Chunk(buf[:n]); err != nil {
				return StreamResult{Cancelled: true, CharsBilled: req.Chars()}, true
			}
			written += n
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			// The header is already on the wire; truncating is all that is left.
			s.logger.Warn().Err(readErr).Str("request_id", req.RequestID).Msg("cached object truncated")
			break
		}
	}

	return StreamResult{
		DurationMS:  entry.DurationMS,
		TTFAMS:      int32(time.Since(started).Milliseconds()), //nolint:gosec // local replay, milliseconds
		CacheHit:    true,
		CharsBilled: req.Chars(),
	}, true
}

// streamFromWorker performs the synthesis and tees it to the cache.
func (s *Service) streamFromWorker(
	ctx context.Context, req Request, key cache.Key, voice voices.Voice, sink StreamSink, lease Lease,
) (StreamResult, error) {
	reservation, err := s.dispatcher.Reserve(ctx, dispatch.ClassStream)
	if err != nil {
		return StreamResult{}, err
	}
	defer reservation.Release()

	// Tied to the client: a disconnect cancels the worker call within one frame
	// (US-09 acceptance criterion 2, T-07).
	workerCtx, cancel := context.WithTimeout(ctx, workerDeadline)
	defer cancel()

	started := time.Now()
	stream, err := reservation.Client.Worker.Synthesize(workerCtx, &workerpb.SynthesizeRequest{
		RequestId: req.RequestID, Text: req.Text, VoiceId: voice.ID,
		Normalize: req.Normalize, Params: toProtoParams(req.Params),
		OutputSampleRate: req.SampleRate,
	})
	if err != nil {
		return StreamResult{}, fmt.Errorf("%w: %w", ErrWorkerFailed, err)
	}

	if err := sink.Header(req.SampleRate); err != nil {
		return StreamResult{}, fmt.Errorf("write header: %w", err)
	}

	var (
		teed      bytes.Buffer
		ttfaMS    int32
		lastSeq   uint32
		seen      bool
		lastRenew = time.Now()
	)

	for {
		frame, recvErr := stream.Recv()
		if errors.Is(recvErr, io.EOF) {
			break
		}
		if recvErr != nil {
			if ctx.Err() != nil {
				// The client hung up. Bill what was delivered, keep nothing.
				result := s.cancelledResult(teed.Len(), req, ttfaMS)
				s.meterStream(context.WithoutCancel(ctx), req, key, voice, result, "client_cancelled")
				return result, nil
			}
			s.meterStream(ctx, req, key, voice, StreamResult{}, "error")
			return StreamResult{}, fmt.Errorf("%w: %w", ErrWorkerFailed, recvErr)
		}

		if seen && frame.GetSeq() != lastSeq+1 {
			s.meterStream(ctx, req, key, voice, StreamResult{}, "error")
			return StreamResult{}, fmt.Errorf("%w: frame %d after %d", ErrWorkerFailed, frame.GetSeq(), lastSeq)
		}
		lastSeq, seen = frame.GetSeq(), true

		if pcm := frame.GetPcm16(); len(pcm) > 0 {
			if ttfaMS == 0 {
				ttfaMS = clampMS(time.Since(started).Milliseconds())
			}
			if err := sink.Chunk(pcm); err != nil {
				// Writing to the client failed: it is gone. Same accounting as a
				// cancelled context.
				cancel()
				result := s.cancelledResult(teed.Len(), req, ttfaMS)
				s.meterStream(context.WithoutCancel(ctx), req, key, voice, result, "client_cancelled")
				return result, nil
			}
			teed.Write(pcm)
		}

		// Renewing per frame rather than per chunk of wall clock keeps a long stream's
		// lease alive without a timer (US-06 acceptance criterion 3).
		if time.Since(lastRenew) > ratelimitRenewEvery {
			if err := lease.Renew(ctx); err != nil {
				s.logger.Warn().Err(err).Str("request_id", req.RequestID).Msg("stream lease lost")
			}
			lastRenew = time.Now()
		}

		if frame.GetLast() {
			break
		}
	}

	durationMS := audio.DurationMS(teed.Len(), req.SampleRate)

	if err := s.cache.Store(context.WithoutCancel(ctx), key, cache.Entry{
		Format: "pcm16", DurationMS: durationMS, Bytes: int64(teed.Len()),
	}, bytes.NewReader(teed.Bytes())); err != nil {
		s.logger.Warn().Err(err).Str("request_id", req.RequestID).Msg("cache store failed")
	}

	result := StreamResult{DurationMS: durationMS, TTFAMS: ttfaMS, CharsBilled: req.Chars()}
	s.meterStream(ctx, req, key, voice, result, "ok")
	return result, nil
}

// ratelimitRenewEvery is how often a running stream refreshes its lease. Well inside the
// 60 s TTL, so one slow frame cannot lose the slot.
const ratelimitRenewEvery = 20 * time.Second

// cancelledResult bills the audio actually delivered (ADR-010).
func (s *Service) cancelledResult(pcmBytes int, req Request, ttfaMS int32) StreamResult {
	durationMS := audio.DurationMS(pcmBytes, req.SampleRate)
	delivered := int64(float64(durationMS) / 1000.0 * charsPerAudioSecond)
	if delivered > req.Chars() {
		delivered = req.Chars()
	}

	return StreamResult{
		DurationMS: durationMS, TTFAMS: ttfaMS,
		CharsBilled: delivered, Cancelled: true,
	}
}

// charsPerAudioSecond is the published estimate from ADR-010. It exists only to meter a
// cancelled stream; a completed one always bills the exact character count.
const charsPerAudioSecond = 14.5

func clampMS(ms int64) int32 {
	if ms > math.MaxInt32 {
		return math.MaxInt32
	}
	if ms < 0 {
		return 0
	}
	return int32(ms) //nolint:gosec // clamped on both sides above
}
