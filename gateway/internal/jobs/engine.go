package jobs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/pcaokhai/vitts/gateway/internal/audio"
	"github.com/pcaokhai/vitts/gateway/internal/cache"
	"github.com/pcaokhai/vitts/gateway/internal/dispatch"
	workerpb "github.com/pcaokhai/vitts/gateway/internal/gen/workerpb"
)

// SegmentStore holds one segment's PCM between synthesis and merge.
type SegmentStore interface {
	PutSegment(ctx context.Context, tenantID, jobID uuid.UUID, seq int32, pcm []byte) (string, error)
	OutputKeyFor(tenantID, jobID uuid.UUID, format string) string
}

// AudioCache lets a job reuse audio an earlier request already produced (ADR-006).
type AudioCache interface {
	Lookup(ctx context.Context, key cache.Key) (cache.Entry, io.ReadCloser, error)
	Store(ctx context.Context, key cache.Key, entry cache.Entry, body io.Reader) error
}

// WorkerEngine performs a job's model work through the dispatcher.
//
// Jobs run at ClassBatch, so an interactive stream always outranks them (ADR-007).
type WorkerEngine struct {
	dispatcher *dispatch.Dispatcher
	segments   SegmentStore
	cache      AudioCache
	logger     zerolog.Logger
}

// NewWorkerEngine wires the engine.
func NewWorkerEngine(
	dispatcher *dispatch.Dispatcher, segments SegmentStore, audioCache AudioCache, logger zerolog.Logger,
) *WorkerEngine {
	return &WorkerEngine{dispatcher: dispatcher, segments: segments, cache: audioCache, logger: logger}
}

// Split asks a worker to chunk the job's text.
func (e *WorkerEngine) Split(ctx context.Context, job Job, text string) ([]string, error) {
	reservation, err := e.dispatcher.Reserve(ctx, dispatch.ClassBatch)
	if err != nil {
		return nil, fmt.Errorf("reserve for segmentation: %w", err)
	}
	defer reservation.Release()

	reply, err := reservation.Client.Worker.Segment(ctx, &workerpb.SegmentRequest{
		Text:      text,
		Normalize: job.Normalize,
	})
	if err != nil {
		return nil, fmt.Errorf("segment: %w", err)
	}
	return reply.GetSegments(), nil
}

// Synthesize renders one segment, reusing cached audio when the same text, voice and
// parameters have been produced before.
func (e *WorkerEngine) Synthesize(ctx context.Context, job Job, seq int32, text string) (SegmentResult, error) {
	key := cache.Derive(cache.Input{
		ModelVersion: job.Metadata[modelVersionKey],
		VoiceID:      job.VoiceID,
		Normalize:    job.Normalize,
		Params:       job.Params,
		Text:         text,
	})

	if pcm, ok := e.fromCache(ctx, key); ok {
		s3Key, err := e.segments.PutSegment(ctx, job.TenantID, job.ID, seq, pcm)
		if err != nil {
			return SegmentResult{}, fmt.Errorf("store cached segment: %w", err)
		}
		return SegmentResult{
			S3Key: s3Key, DurationMS: audio.DurationMS(len(pcm), job.SampleRate), Cached: true,
		}, nil
	}

	pcm, err := e.synthesize(ctx, job, text)
	if err != nil {
		return SegmentResult{}, err
	}

	s3Key, err := e.segments.PutSegment(ctx, job.TenantID, job.ID, seq, pcm)
	if err != nil {
		return SegmentResult{}, fmt.Errorf("store segment: %w", err)
	}

	durationMS := audio.DurationMS(len(pcm), job.SampleRate)
	if err := e.cache.Store(ctx, key, cache.Entry{
		Format: "pcm16", DurationMS: durationMS, Bytes: int64(len(pcm)),
	}, bytes.NewReader(pcm)); err != nil {
		// The segment is already stored and correct; a cache miss next time is the only
		// cost of this failing.
		e.logger.Warn().Err(err).Str("job_id", job.ID.String()).Msg("segment not cached")
	}

	return SegmentResult{S3Key: s3Key, DurationMS: durationMS}, nil
}

func (e *WorkerEngine) fromCache(ctx context.Context, key cache.Key) ([]byte, bool) {
	entry, body, err := e.cache.Lookup(ctx, key)
	if err != nil {
		return nil, false
	}
	defer func() { _ = body.Close() }()

	pcm, err := io.ReadAll(body)
	if err != nil || len(pcm) == 0 {
		return nil, false
	}
	_ = entry
	return pcm, true
}

func (e *WorkerEngine) synthesize(ctx context.Context, job Job, text string) ([]byte, error) {
	reservation, err := e.dispatcher.Reserve(ctx, dispatch.ClassBatch)
	if err != nil {
		return nil, fmt.Errorf("reserve for segment: %w", err)
	}
	defer reservation.Release()

	stream, err := reservation.Client.Worker.Synthesize(ctx, &workerpb.SynthesizeRequest{
		RequestId:        job.ID.String(),
		Text:             text,
		VoiceId:          job.VoiceID,
		Normalize:        job.Normalize,
		OutputSampleRate: job.SampleRate,
	})
	if err != nil {
		return nil, fmt.Errorf("synthesize segment: %w", err)
	}

	var pcm bytes.Buffer
	for {
		frame, recvErr := stream.Recv()
		if errors.Is(recvErr, io.EOF) {
			break
		}
		if recvErr != nil {
			return nil, fmt.Errorf("receive segment audio: %w", recvErr)
		}
		pcm.Write(frame.GetPcm16())
		if frame.GetLast() {
			break
		}
	}

	if pcm.Len() == 0 {
		return nil, errors.New("worker produced no audio")
	}
	return pcm.Bytes(), nil
}

// Merge concatenates the job's segments into its output format.
//
// The worker reads the segment objects and writes the output itself, so a long job's
// audio never crosses the gateway.
func (e *WorkerEngine) Merge(ctx context.Context, job Job, segmentKeys []string) (string, int32, error) {
	reservation, err := e.dispatcher.Reserve(ctx, dispatch.ClassBatch)
	if err != nil {
		return "", 0, fmt.Errorf("reserve for merge: %w", err)
	}
	defer reservation.Release()

	outputKey := e.segments.OutputKeyFor(job.TenantID, job.ID, job.Format)
	reply, err := reservation.Client.Worker.Merge(ctx, &workerpb.MergeRequest{
		S3Keys:      segmentKeys,
		OutputS3Key: outputKey,
		Format:      job.Format,
		SampleRate:  job.SampleRate,
	})
	if err != nil {
		return "", 0, fmt.Errorf("merge: %w", err)
	}

	duration := reply.GetDurationMs()
	if duration > int64(int32(^uint32(0)>>1)) {
		duration = int64(int32(^uint32(0) >> 1))
	}
	return reply.GetOutputS3Key(), int32(duration), nil //nolint:gosec // clamped above
}
