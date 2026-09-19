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
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

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
	// Tied to the client: a disconnect cancels the worker call within one frame
	// (US-09 acceptance criterion 2, T-07).
	workerCtx, cancel := context.WithTimeout(ctx, workerDeadline)
	defer cancel()

	reservation, stream, firstFrame, started, err := s.openStream(ctx, workerCtx, req, voice)
	if err != nil {
		return StreamResult{}, err
	}
	defer reservation.Release()

	var (
		// The header is written when the first audio arrives, not before: a refusal
		// before any audio must become a clean error, not a truncated stream.
		headerWritten bool
		teed          bytes.Buffer
		ttfaMS        int32
		lastSeq       uint32
		seen          bool
		lastRenew     = time.Now()
	)

	frame := firstFrame
	for {
		if frame == nil {
			received, recvErr := stream.Recv()
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
				return StreamResult{}, classifyWorkerError(recvErr)
			}
			frame = received
		}

		if seen && frame.GetSeq() != lastSeq+1 {
			s.meterStream(ctx, req, key, voice, StreamResult{}, "error")
			return StreamResult{}, fmt.Errorf("%w: frame %d after %d", ErrWorkerFailed, frame.GetSeq(), lastSeq)
		}
		lastSeq, seen = frame.GetSeq(), true

		if pcm := frame.GetPcm16(); len(pcm) > 0 {
			if !headerWritten {
				if err := sink.Header(req.SampleRate); err != nil {
					return StreamResult{}, fmt.Errorf("write header: %w", err)
				}
				headerWritten = true
			}
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

		// Renewing on a timer inside the loop keeps a long stream's lease alive without
		// a separate goroutine (US-06 acceptance criterion 3).
		if time.Since(lastRenew) > leaseRenewEvery {
			if err := lease.Renew(ctx); err != nil {
				s.logger.Warn().Err(err).Str("request_id", req.RequestID).Msg("stream lease lost")
			}
			lastRenew = time.Now()
		}

		if frame.GetLast() {
			break
		}
		frame = nil
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

// openStream reserves a slot and opens the worker stream, retrying when the worker
// refuses a slot the pool had advertised.
//
// Opening a server stream does not round-trip, so a refusal only surfaces on the first
// Recv — which is why the first frame is read here and handed back to the caller. The
// reservation is returned still held: a stream owns its slot for its whole life.
func (s *Service) openStream(
	ctx, workerCtx context.Context, req Request, voice voices.Voice,
) (*dispatch.Reservation, workerpb.Worker_SynthesizeClient, *workerpb.AudioFrame, time.Time, error) {
	var lastErr error

	for attempt := range workerAttempts {
		reservation, err := s.dispatcher.Reserve(ctx, dispatch.ClassStream)
		if err != nil {
			return nil, nil, nil, time.Time{}, err
		}

		started := time.Now()
		stream, err := reservation.Client.Worker.Synthesize(workerCtx, &workerpb.SynthesizeRequest{
			RequestId: req.RequestID, Text: req.Text, VoiceId: voice.ID,
			Normalize: req.Normalize, Params: toProtoParams(req.Params),
			OutputSampleRate: req.SampleRate,
		})
		if err == nil {
			var first *workerpb.AudioFrame
			first, err = stream.Recv()
			if err == nil {
				return reservation, stream, first, started, nil
			}
		}

		reservation.Release()
		if status.Code(err) != codes.ResourceExhausted {
			return nil, nil, nil, time.Time{}, classifyWorkerError(err)
		}

		lastErr = err
		s.logger.Debug().Str("request_id", req.RequestID).Int("attempt", attempt+1).
			Msg("worker refused an advertised slot, re-reserving")

		if err := sleepCtx(ctx, slotRetryBackoff); err != nil {
			return nil, nil, nil, time.Time{}, err
		}
	}

	return nil, nil, nil, time.Time{},
		fmt.Errorf("%w: worker slot taken on every attempt: %w", dispatch.ErrOverloaded, lastErr)
}

// leaseRenewEvery is how often a running stream refreshes its lease. Well inside the
// 60 s TTL, so one slow frame cannot lose the slot.
const leaseRenewEvery = 20 * time.Second

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

// classifyWorkerError turns a gRPC failure into the error the transport should surface.
//
// RESOURCE_EXHAUSTED is the worker refusing a slot it had already advertised as free: the
// pool's view is up to one health poll stale, and the worker is the authority (ADR-003).
// That is overload, not a broken worker, so the caller is told to retry.
func classifyWorkerError(err error) error {
	if status.Code(err) == codes.ResourceExhausted {
		return fmt.Errorf("%w: worker slot taken", dispatch.ErrOverloaded)
	}
	return fmt.Errorf("%w: %w", ErrWorkerFailed, err)
}
