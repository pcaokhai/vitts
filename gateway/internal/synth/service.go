package synth

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/pcaokhai/vitts/gateway/internal/audio"
	"github.com/pcaokhai/vitts/gateway/internal/cache"
	"github.com/pcaokhai/vitts/gateway/internal/dispatch"
	workerpb "github.com/pcaokhai/vitts/gateway/internal/gen/workerpb"
	"github.com/pcaokhai/vitts/gateway/internal/telemetry"
	"github.com/pcaokhai/vitts/gateway/internal/voices"
)

// workerDeadline bounds one synthesis. At the measured RTF a 3,000-character request is
// well inside this; past it the worker is stuck and the slot is better reclaimed.
const workerDeadline = 60 * time.Second

// Allowance is what the quota service answers.
type Allowance struct {
	Allowed bool
	Used    int64
}

// Quota is the allowance port.
type Quota interface {
	Check(ctx context.Context, tenantID uuid.UUID, planLimit, chars int64) (Allowance, error)
	Record(ctx context.Context, tenantID uuid.UUID, chars int64) error
}

// Cache is the audio cache port.
type Cache interface {
	Lookup(ctx context.Context, key cache.Key) (cache.Entry, io.ReadCloser, error)
	Store(ctx context.Context, key cache.Key, entry cache.Entry, body io.Reader) error
}

// Dispatcher admits work to the fleet.
type Dispatcher interface {
	Reserve(ctx context.Context, class dispatch.Class) (*dispatch.Reservation, error)
}

// Catalogue resolves the requested voice.
type Catalogue interface {
	Resolve(ctx context.Context, tenantID *uuid.UUID, id string) (voices.Voice, error)
}

// PlanAllowance answers how many characters a plan includes.
type PlanAllowance interface {
	CharsPerMonth(ctx context.Context, planID string) (int64, error)
}

// Meter records what a request consumed. Implemented in task 1.12; a no-op meter keeps
// this package usable before then.
type Meter interface {
	Record(ctx context.Context, usage Usage)
}

// Usage is one metered request.
type Usage struct {
	TenantID   uuid.UUID
	KeyID      uuid.UUID
	RequestID  string
	VoiceID    string
	CacheKey   []byte
	Mode       string
	Chars      int64
	DurationMS int32
	TTFAMS     int32
	Cached     bool
	Status     string
}

// Service runs a synthesis end to end.
type Service struct {
	quota      Quota
	cache      Cache
	dispatcher Dispatcher
	catalogue  Catalogue
	plans      PlanAllowance
	meter      Meter
	logger     zerolog.Logger
	metrics    *telemetry.Metrics
}

// SetMetrics attaches the instrument set. Separate from the constructor because metrics
// are optional: tests run without them, and a nil set means the service records nothing.
func (s *Service) SetMetrics(metrics *telemetry.Metrics) { s.metrics = metrics }

// observe records what one synthesis cost, in the shape US-18 asks for.
func (s *Service) observe(mode string, tenantID uuid.UUID, chars int64, ttfaMS, durationMS int32, elapsed time.Duration, cached bool) {
	if s.metrics == nil {
		return
	}

	result := "miss"
	if cached {
		result = "hit"
	}
	s.metrics.CacheTotal.WithLabelValues(result).Inc()
	s.metrics.UsageCharsTotal.
		WithLabelValues(telemetry.TenantLabel(tenantID.String()), mode).
		Add(float64(chars))

	if ttfaMS > 0 {
		s.metrics.TTFASeconds.WithLabelValues(mode).Observe(telemetry.MillisecondsToSeconds(ttfaMS))
	}
	// RTF is only meaningful for work we actually did: a cache hit would report a
	// flattering number that says nothing about the fleet.
	if !cached && durationMS > 0 {
		s.metrics.RTF.WithLabelValues(mode).Observe(elapsed.Seconds() / (float64(durationMS) / 1000))
	}
}

// NewService wires the use case.
func NewService(
	quota Quota, cache Cache, dispatcher Dispatcher, catalogue Catalogue,
	plans PlanAllowance, meter Meter, logger zerolog.Logger,
) *Service {
	return &Service{
		quota: quota, cache: cache, dispatcher: dispatcher,
		catalogue: catalogue, plans: plans, meter: meter, logger: logger,
	}
}

// Synthesize returns a complete audio file (US-08, FL-01).
//
// Order follows FL-01: validate, resolve the voice, check quota, look in the cache, and
// only then take a dispatch slot. Every step before dispatch is cheap and can refuse the
// request without costing the fleet anything.
func (s *Service) Synthesize(ctx context.Context, req Request, planID string) (Result, error) {
	if err := req.Validate(); err != nil {
		return Result{}, err
	}

	voice, err := s.resolveVoice(ctx, req)
	if err != nil {
		return Result{}, err
	}

	if err := s.checkQuota(ctx, req, planID); err != nil {
		return Result{}, err
	}

	key := cache.Derive(cache.Input{
		ModelVersion: voice.ModelVersion,
		VoiceID:      voice.ID,
		Normalize:    req.Normalize,
		Params:       req.Params,
		Text:         req.Text,
	})

	if result, ok := s.fromCache(ctx, req, key, voice); ok {
		return result, nil
	}

	return s.fromWorker(ctx, req, key, voice)
}

// fromCache answers a hit. A cache failure is never fatal: re-synthesising is always
// correct, just slower (US-11).
func (s *Service) fromCache(ctx context.Context, req Request, key cache.Key, voice voices.Voice) (Result, bool) {
	entry, body, err := s.cache.Lookup(ctx, key)
	if err != nil {
		if !errors.Is(err, cache.ErrMiss) {
			s.logger.Warn().Err(err).Str("request_id", req.RequestID).Msg("cache lookup failed")
		}
		return Result{}, false
	}
	defer func() { _ = body.Close() }()

	pcm, err := io.ReadAll(body)
	if err != nil {
		s.logger.Warn().Err(err).Str("request_id", req.RequestID).Msg("cached object unreadable")
		return Result{}, false
	}

	wav, err := wrap(pcm, req.SampleRate)
	if err != nil {
		return Result{}, false
	}

	// A hit still bills: the tenant received the audio, and cache economics are ours,
	// not theirs (ADR-006).
	s.bill(ctx, req, key, voice, entry.DurationMS, true, "ok")
	s.observe("sync", req.TenantID, req.Chars(), 0, entry.DurationMS, 0, true)

	return Result{
		Audio: wav, DurationMS: entry.DurationMS, SampleRate: req.SampleRate,
		Format: req.Format, CacheHit: true, CharsBilled: req.Chars(),
		VoiceID: voice.ID, ModelVersion: voice.ModelVersion,
	}, true
}

// fromWorker performs the synthesis.
func (s *Service) fromWorker(ctx context.Context, req Request, key cache.Key, voice voices.Voice) (Result, error) {
	ctx, cancel := context.WithTimeout(ctx, workerDeadline)
	defer cancel()

	var (
		pcm     bytes.Buffer
		ttfaMS  int32
		started time.Time
	)

	err := s.withWorker(ctx, dispatch.ClassSync, req.RequestID, func(client dispatch.Client) error {
		pcm.Reset()
		ttfaMS = 0
		started = time.Now()

		stream, err := client.Worker.Synthesize(ctx, &workerpb.SynthesizeRequest{
			RequestId:        req.RequestID,
			Text:             req.Text,
			VoiceId:          voice.ID,
			Normalize:        req.Normalize,
			Params:           toProtoParams(req.Params),
			OutputSampleRate: req.SampleRate,
		})
		if err != nil {
			return err //nolint:wrapcheck // withWorker inspects the gRPC status code
		}

		var (
			lastSeq uint32
			seen    bool
		)
		for {
			frame, recvErr := stream.Recv()
			if errors.Is(recvErr, io.EOF) {
				return nil
			}
			if recvErr != nil {
				return recvErr //nolint:wrapcheck // withWorker inspects the gRPC status code
			}

			if seen && frame.GetSeq() != lastSeq+1 {
				// Out-of-order frames mean the audio would be wrong; failing beats
				// returning something that sounds broken (US-09 acceptance criterion 3).
				return fmt.Errorf("%w: frame %d after %d", ErrWorkerFailed, frame.GetSeq(), lastSeq)
			}
			lastSeq, seen = frame.GetSeq(), true

			if len(frame.GetPcm16()) > 0 && ttfaMS == 0 {
				ttfaMS = clampMS(time.Since(started).Milliseconds())
			}
			pcm.Write(frame.GetPcm16())

			if frame.GetLast() {
				return nil
			}
		}
	})
	if err != nil {
		s.bill(ctx, req, key, voice, 0, false, "error")
		return Result{}, classifyWorkerError(err)
	}

	durationMS := audio.DurationMS(pcm.Len(), req.SampleRate)

	// A cache write must not fail the request the tenant is waiting on: the audio is
	// already correct, and a missing entry only costs the next caller.
	if err := s.cache.Store(ctx, key, cache.Entry{
		Format:     "pcm16",
		DurationMS: durationMS,
		Bytes:      int64(pcm.Len()),
	}, bytes.NewReader(pcm.Bytes())); err != nil {
		s.logger.Warn().Err(err).Str("request_id", req.RequestID).Msg("cache store failed")
	}

	s.billWithTTFA(ctx, req, key, voice, durationMS, ttfaMS, false, "ok")
	s.observe("sync", req.TenantID, req.Chars(), ttfaMS, durationMS, time.Since(started), false)

	wav, err := wrap(pcm.Bytes(), req.SampleRate)
	if err != nil {
		return Result{}, err
	}

	return Result{
		Audio: wav, DurationMS: durationMS, SampleRate: req.SampleRate,
		Format: req.Format, CacheHit: false, CharsBilled: req.Chars(),
		VoiceID: voice.ID, ModelVersion: voice.ModelVersion,
	}, nil
}

// resolveVoice turns a requested voice id into a catalogue entry.
func (s *Service) resolveVoice(ctx context.Context, req Request) (voices.Voice, error) {
	voice, err := s.catalogue.Resolve(ctx, &req.TenantID, req.VoiceID)
	if err != nil {
		if errors.Is(err, voices.ErrUnknown) {
			return voices.Voice{}, fmt.Errorf("%w: %s", ErrUnknownVoice, req.VoiceID)
		}
		return voices.Voice{}, fmt.Errorf("resolve voice: %w", err)
	}
	return voice, nil
}

// checkQuota refuses a request that would take the tenant past its allowance. It
// consumes nothing: the counter moves only after delivery (US-07 acceptance criterion 2).
func (s *Service) checkQuota(ctx context.Context, req Request, planID string) error {
	allowance, err := s.plans.CharsPerMonth(ctx, planID)
	if err != nil {
		return fmt.Errorf("read plan allowance: %w", err)
	}

	decision, err := s.quota.Check(ctx, req.TenantID, allowance, req.Chars())
	if err != nil {
		return fmt.Errorf("check quota: %w", err)
	}
	if !decision.Allowed {
		return fmt.Errorf("%w: used %d of %d", ErrQuotaExceeded, decision.Used, allowance)
	}
	return nil
}

// meterStream records a stream's quota and usage. A cancelled stream bills the audio it
// delivered rather than the text it was asked for (ADR-010).
func (s *Service) meterStream(
	ctx context.Context, req Request, key cache.Key, voice voices.Voice,
	result StreamResult, status string,
) {
	if status == "ok" || status == "client_cancelled" {
		if err := s.quota.Record(ctx, req.TenantID, result.CharsBilled); err != nil {
			s.logger.Error().Err(err).Str("request_id", req.RequestID).Msg("quota not recorded")
		}
	}

	s.meter.Record(ctx, Usage{
		TenantID: req.TenantID, KeyID: req.KeyID, RequestID: req.RequestID,
		VoiceID: voice.ID, CacheKey: key.Bytes(), Mode: "stream",
		Chars: result.CharsBilled, DurationMS: result.DurationMS, TTFAMS: result.TTFAMS,
		Cached: result.CacheHit, Status: status,
	})
}

func (s *Service) bill(ctx context.Context, req Request, key cache.Key, voice voices.Voice, durationMS int32, cached bool, status string) {
	s.billWithTTFA(ctx, req, key, voice, durationMS, 0, cached, status)
}

// billWithTTFA records quota and usage. Quota is recorded only for a delivered
// synthesis: a failed request must not consume the tenant's allowance (US-07 AC-2).
func (s *Service) billWithTTFA(
	ctx context.Context, req Request, key cache.Key, voice voices.Voice,
	durationMS, ttfaMS int32, cached bool, status string,
) {
	if status == "ok" {
		if err := s.quota.Record(ctx, req.TenantID, req.Chars()); err != nil {
			s.logger.Error().Err(err).Str("request_id", req.RequestID).Msg("quota not recorded")
		}
	}

	s.meter.Record(ctx, Usage{
		TenantID: req.TenantID, KeyID: req.KeyID, RequestID: req.RequestID,
		VoiceID: voice.ID, CacheKey: key.Bytes(), Mode: "sync",
		Chars: req.Chars(), DurationMS: durationMS, TTFAMS: ttfaMS,
		Cached: cached, Status: status,
	})
}

// workerAttempts is how many times a request may re-reserve after a worker refuses a
// slot it had advertised. The pool's view lags one health poll, so a refusal usually
// means "a moment too early", and Reserve then waits inside the class budget for the
// slot to free rather than failing the caller (ADR-003, ADR-007).
const workerAttempts = 3

// slotRetryBackoff is how long to wait before re-reserving after a worker refuses.
//
// A cancelled inference releases its slot only once the in-flight frame finishes, which
// the worker measured at well under 200 ms. Retrying immediately just collects the same
// refusal three times; three spaced attempts stay inside the stream class's 2 s budget.
const slotRetryBackoff = 150 * time.Millisecond

// withWorker reserves a slot and runs fn, retrying on a worker's RESOURCE_EXHAUSTED.
//
// It never waits beyond the dispatcher's class budget: each retry goes back through
// Reserve, which owns that budget, so a genuinely saturated fleet still fails fast.
func (s *Service) withWorker(
	ctx context.Context, class dispatch.Class, requestID string,
	fn func(client dispatch.Client) error,
) error {
	var lastErr error
	for attempt := range workerAttempts {
		reservation, err := s.dispatcher.Reserve(ctx, class)
		if err != nil {
			return err
		}

		err = fn(reservation.Client)
		reservation.Release()

		if err == nil {
			return nil
		}
		if status.Code(err) != codes.ResourceExhausted {
			return err
		}

		lastErr = err
		s.logger.Debug().
			Str("request_id", requestID).
			Str("worker", reservation.Client.Addr).
			Int("attempt", attempt+1).
			Msg("worker refused an advertised slot, re-reserving")

		if err := sleepCtx(ctx, slotRetryBackoff); err != nil {
			return err
		}
	}

	return fmt.Errorf("%w: worker slot taken on every attempt: %w", dispatch.ErrOverloaded, lastErr)
}

func wrap(pcm []byte, sampleRate int32) ([]byte, error) {
	size, err := audio.PCMSize(len(pcm))
	if err != nil {
		return nil, fmt.Errorf("wrap audio: %w", err)
	}

	var out bytes.Buffer
	out.Grow(len(pcm) + audio.HeaderSize)
	if err := audio.WriteHeader(&out, sampleRate, size); err != nil {
		return nil, err
	}
	out.Write(pcm)
	return out.Bytes(), nil
}

func toProtoParams(p cache.Params) *workerpb.SynthesisParams {
	return &workerpb.SynthesisParams{
		CfgScale:               float32(p.CFGScale),
		AudioTemperature:       float32(p.AudioTemperature),
		AudioTopk:              p.AudioTopK,
		AudioTopp:              float32(p.AudioTopP),
		AudioRepetitionPenalty: float32(p.AudioRepetitionPenalty),
		EoaExtraFrames:         p.EOAExtraFrames,
	}
}

// sleepCtx waits for d unless the caller goes away first.
func sleepCtx(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return fmt.Errorf("wait for worker slot: %w", ctx.Err())
	case <-timer.C:
		return nil
	}
}
