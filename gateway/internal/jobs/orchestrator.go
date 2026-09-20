package jobs

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/pcaokhai/vitts/gateway/internal/cache"
	"github.com/pcaokhai/vitts/gateway/internal/telemetry"
)

// Orchestrator timings from FL-03.
const (
	// MaxSegmentAttempts is how many times a segment is tried before the job fails
	// (US-14 acceptance criterion 3).
	MaxSegmentAttempts int32 = 3
	// ReconcileInterval is how often abandoned work is swept back into the queue.
	ReconcileInterval = 60 * time.Second
	// StuckAfter is how long a live job may go unchanged before the reconciler
	// re-queues it.
	StuckAfter = 2 * time.Minute
	// claimBlock is how long a consumer waits on an empty stream before looping, which
	// bounds how quickly it notices shutdown.
	claimBlock = 2 * time.Second
	// claimBatch is how many entries one read takes.
	claimBatch = 16
)

// SegmentBackoff is the delay before each retry (FL-03 step 8).
var SegmentBackoff = []time.Duration{5 * time.Second, 30 * time.Second, 120 * time.Second}

// Stream names the orchestrator consumes.
const (
	StreamJobs     = "jobs:pending"
	StreamSegments = "jobs:segments"
)

// QueueEntry is one claimed unit of work.
type QueueEntry struct {
	ID     string
	JobID  uuid.UUID
	Seq    int32
	Stream string
}

// WorkQueue is the stream port.
type WorkQueue interface {
	Enqueue(ctx context.Context, jobID uuid.UUID) error
	EnqueueSegment(ctx context.Context, jobID uuid.UUID, seq int32) error
	Claim(ctx context.Context, stream, consumer string, count int64, block time.Duration) ([]QueueEntry, error)
	ClaimStale(ctx context.Context, stream, consumer string, idle time.Duration, count int64) ([]QueueEntry, error)
	Ack(ctx context.Context, stream, id string) error
}

// StateStore is the orchestrator's half of the jobs aggregate.
type StateStore interface {
	Get(ctx context.Context, tenantID, jobID uuid.UUID) (Job, error)
	ByID(ctx context.Context, jobID uuid.UUID) (Job, error)
	Transition(ctx context.Context, jobID uuid.UUID, from, to Status) (bool, error)
	SetSegmentsTotal(ctx context.Context, jobID uuid.UUID, total int32) (bool, error)
	CreateSegment(ctx context.Context, jobID uuid.UUID, seq int32, textHash []byte, chars int32) error
	Segments(ctx context.Context, jobID uuid.UUID) ([]Segment, error)
	ClaimSegment(ctx context.Context, jobID uuid.UUID, seq int32) (bool, error)
	CompleteSegment(ctx context.Context, jobID uuid.UUID, seq int32, s3Key string) error
	FailSegment(ctx context.Context, jobID uuid.UUID, seq int32, reason string) error
	UnfinishedSegments(ctx context.Context, jobID uuid.UUID) (int64, error)
	Fail(ctx context.Context, jobID uuid.UUID, reason string) error
	Complete(ctx context.Context, jobID uuid.UUID, outputKey string, durationMS int32) (bool, error)
	Stuck(ctx context.Context, before time.Time, limit int32) ([]Job, error)
}

// Texts reads a job's input and removes it once the job is done.
type Texts interface {
	GetText(ctx context.Context, tenantID, jobID uuid.UUID) (string, error)
	DeleteText(ctx context.Context, tenantID, jobID uuid.UUID) error
}

// SegmentResult is what synthesizing one segment produced.
type SegmentResult struct {
	S3Key      string
	DurationMS int32
	Cached     bool
}

// Engine performs the model-facing work. It hides dispatch, the worker contract and the
// cache from the state machine.
type Engine interface {
	// Split asks a worker to segment the job's text.
	Split(ctx context.Context, job Job, text string) ([]string, error)
	// Synthesize renders one segment and stores its PCM, returning where it landed.
	Synthesize(ctx context.Context, job Job, seq int32, text string) (SegmentResult, error)
	// Merge concatenates the segments into the job's output format.
	Merge(ctx context.Context, job Job, segmentKeys []string) (outputKey string, durationMS int32, err error)
}

// Notifier is told when a job reaches a terminal state. Implemented by the webhook
// dispatcher in task 2.5; a nil notifier simply means nobody is told.
type Notifier interface {
	JobFinished(ctx context.Context, job Job)
}

// Orchestrator drives jobs from queued to completed (FL-03).
//
// It is a set of small, idempotent steps rather than one long transaction: a job's state
// lives in Postgres, so a crash at any point resumes from the row rather than restarting
// the work (US-14 acceptance criterion 6).
type Orchestrator struct {
	queue    WorkQueue
	store    StateStore
	texts    Texts
	engine   Engine
	notifier Notifier
	logger   zerolog.Logger
	consumer string
	metrics  *telemetry.Metrics
	// sleep is injectable so retry backoff can be exercised without waiting.
	sleep func(ctx context.Context, d time.Duration) error
}

// SetMetrics attaches the instrument set. Optional, so tests run without one.
func (o *Orchestrator) SetMetrics(metrics *telemetry.Metrics) { o.metrics = metrics }

// observeTerminal records a finished job, which is what an alert on job failure rate
// and a dashboard of job duration are built from.
func (o *Orchestrator) observeTerminal(job Job, status Status) {
	if o.metrics == nil {
		return
	}
	o.metrics.JobsTotal.WithLabelValues(string(status)).Inc()
	if !job.CreatedAt.IsZero() {
		o.metrics.JobDuration.WithLabelValues(string(status)).Observe(time.Since(job.CreatedAt).Seconds())
	}
}

// NewOrchestrator wires the state machine.
func NewOrchestrator(
	queue WorkQueue, store StateStore, texts Texts, engine Engine,
	notifier Notifier, consumer string, logger zerolog.Logger,
) *Orchestrator {
	return &Orchestrator{
		queue: queue, store: store, texts: texts, engine: engine,
		notifier: notifier, consumer: consumer, logger: logger, sleep: sleepCtx,
	}
}

// Run consumes both streams and reconciles until the context is cancelled.
func (o *Orchestrator) Run(ctx context.Context) {
	go o.consume(ctx, StreamJobs, o.handleJob)
	go o.consume(ctx, StreamSegments, o.handleSegment)
	go o.reconcileLoop(ctx)
}

func (o *Orchestrator) consume(ctx context.Context, stream string, handle func(context.Context, QueueEntry) error) {
	for ctx.Err() == nil {
		entries, err := o.queue.Claim(ctx, stream, o.consumer, claimBatch, claimBlock)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			o.logger.Error().Err(err).Str("stream", stream).Msg("claim failed")
			if sleepErr := o.sleep(ctx, time.Second); sleepErr != nil {
				return
			}
			continue
		}

		for _, entry := range entries {
			if err := handle(ctx, entry); err != nil && ctx.Err() == nil {
				o.logger.Error().Err(err).
					Str("stream", stream).Str("job_id", entry.JobID.String()).
					Msg("work item failed")
			}
			// Acknowledged either way: a failure is recorded on the job row, and leaving
			// the entry pending would have the reconciler replay it forever.
			if err := o.queue.Ack(ctx, stream, entry.ID); err != nil && ctx.Err() == nil {
				o.logger.Warn().Err(err).Str("stream", stream).Msg("ack failed")
			}
		}
	}
}

// handleJob segments a queued job and fans its segments out (FL-03 steps 5-7).
func (o *Orchestrator) handleJob(ctx context.Context, entry QueueEntry) error {
	job, err := o.store.ByID(ctx, entry.JobID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil // the row is gone; nothing to do
		}
		return fmt.Errorf("load job: %w", err)
	}
	if job.Status.Terminal() {
		return nil // cancelled or already finished while it sat in the stream
	}

	// Already segmented: this is a replay, so go straight to fanning out whatever is
	// still unfinished (T-09).
	if job.Status == StatusSynthesizing || job.Status == StatusMerging {
		return o.fanOut(ctx, job)
	}

	// A job already in segmenting is one whose orchestrator died between the transition
	// and the segment rows. Resuming is safe — segment inserts are `on conflict do
	// nothing` — and it is the only way out: the reconciler re-queues such a job forever,
	// and treating it as "someone else has it" left it re-queued and ignored every minute
	// until a human noticed (docs/reports/drill-m3.md, finding 6).
	if job.Status != StatusSegmenting {
		if moved, err := o.store.Transition(ctx, job.ID, StatusQueued, StatusSegmenting); err != nil {
			return err
		} else if !moved {
			return nil // another orchestrator took it
		}
	}

	text, err := o.texts.GetText(ctx, job.TenantID, job.ID)
	if err != nil {
		return o.failJob(ctx, job, fmt.Sprintf("input text unavailable: %v", err))
	}

	segments, err := o.engine.Split(ctx, job, text)
	if err != nil {
		return o.failJob(ctx, job, fmt.Sprintf("segmentation failed: %v", err))
	}
	if len(segments) == 0 {
		return o.failJob(ctx, job, "segmentation produced nothing")
	}

	// Bounded by the text limit: 100k characters cannot produce more segments than
	// characters, so the counter cannot approach int32.
	if len(segments) > MaxTextChars {
		return o.failJob(ctx, job, fmt.Sprintf("segmentation produced %d segments", len(segments)))
	}

	for seq, segmentText := range segments {
		hash := segmentHash(job, segmentText)
		//nolint:gosec // bounded by the check above
		if err := o.store.CreateSegment(ctx, job.ID, int32(seq), hash, runeCount([]rune(segmentText))); err != nil {
			return fmt.Errorf("record segment %d: %w", seq, err)
		}
	}

	total := int32(len(segments)) //nolint:gosec // bounded by the check above
	if _, err := o.store.SetSegmentsTotal(ctx, job.ID, total); err != nil {
		return err
	}

	refreshed, err := o.store.ByID(ctx, job.ID)
	if err != nil {
		return fmt.Errorf("reload job: %w", err)
	}
	return o.fanOut(ctx, refreshed)
}

// fanOut queues every segment that is not done. Safe to repeat: a done segment is never
// re-queued, and a claimed one is skipped by ClaimSegment.
func (o *Orchestrator) fanOut(ctx context.Context, job Job) error {
	segments, err := o.store.Segments(ctx, job.ID)
	if err != nil {
		return err
	}

	var queued int
	for _, segment := range segments {
		if segment.Status == SegmentDone {
			continue
		}
		if err := o.queue.EnqueueSegment(ctx, job.ID, segment.Seq); err != nil {
			return fmt.Errorf("queue segment %d: %w", segment.Seq, err)
		}
		queued++
	}

	o.logger.Info().Str("job_id", job.ID.String()).
		Int("segments", len(segments)).Int("queued", queued).Msg("job fanned out")

	if queued == 0 {
		// Everything was already done, which happens on a replay after a crash: move
		// straight to merging rather than waiting for a segment that will never arrive.
		return o.finish(ctx, job)
	}
	return nil
}

// handleSegment synthesizes one segment (FL-03 step 8).
func (o *Orchestrator) handleSegment(ctx context.Context, entry QueueEntry) error {
	job, err := o.store.ByID(ctx, entry.JobID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		return fmt.Errorf("load job: %w", err)
	}
	// A cancelled job must not dispatch more segments (US-14 acceptance criterion 5).
	if job.Status.Terminal() {
		return nil
	}

	claimed, err := o.store.ClaimSegment(ctx, job.ID, entry.Seq)
	if err != nil {
		return err
	}
	if !claimed {
		// Already done, or running elsewhere. This is what makes a replayed entry cost
		// nothing instead of re-synthesizing (T-09).
		return nil
	}

	segments, err := o.store.Segments(ctx, job.ID)
	if err != nil {
		return err
	}
	current, ok := findSegment(segments, entry.Seq)
	if !ok {
		return fmt.Errorf("segment %d of job %s is missing", entry.Seq, job.ID)
	}

	text, err := o.texts.GetText(ctx, job.TenantID, job.ID)
	if err != nil {
		return o.retryOrFail(ctx, job, current, fmt.Sprintf("input text unavailable: %v", err))
	}
	pieces, err := o.engine.Split(ctx, job, text)
	if err != nil || int(entry.Seq) >= len(pieces) {
		return o.retryOrFail(ctx, job, current, "segment text could not be recovered")
	}

	result, err := o.engine.Synthesize(ctx, job, entry.Seq, pieces[entry.Seq])
	if err != nil {
		return o.retryOrFail(ctx, job, current, err.Error())
	}

	if err := o.store.CompleteSegment(ctx, job.ID, entry.Seq, result.S3Key); err != nil {
		return err
	}

	remaining, err := o.store.UnfinishedSegments(ctx, job.ID)
	if err != nil {
		return err
	}
	if remaining > 0 {
		return nil
	}

	refreshed, err := o.store.ByID(ctx, job.ID)
	if err != nil {
		return fmt.Errorf("reload job: %w", err)
	}
	return o.finish(ctx, refreshed)
}

// retryOrFail re-queues a segment with backoff, or fails the job once it is out of
// attempts (US-14 acceptance criterion 3).
func (o *Orchestrator) retryOrFail(ctx context.Context, job Job, segment Segment, reason string) error {
	if err := o.store.FailSegment(ctx, job.ID, segment.Seq, reason); err != nil {
		return err
	}

	// Attempts already counts this try: the claim incremented it before the work ran.
	if segment.Attempts >= MaxSegmentAttempts {
		return o.failJob(ctx, job, fmt.Sprintf("segment %d failed after %d attempts: %s",
			segment.Seq, segment.Attempts, reason))
	}

	backoff := SegmentBackoff[min(int(segment.Attempts)-1, len(SegmentBackoff)-1)]
	o.logger.Warn().Str("job_id", job.ID.String()).Int32("seq", segment.Seq).
		Int32("attempt", segment.Attempts).Dur("backoff", backoff).
		Str("reason", reason).Msg("segment failed, retrying")

	// Waiting here holds one consumer slot rather than adding a delayed-queue mechanism
	// the design does not have; the backoffs are short next to a job's lifetime.
	if err := o.sleep(ctx, backoff); err != nil {
		return nil // shutting down; the reconciler will pick this up
	}
	return o.queue.EnqueueSegment(ctx, job.ID, segment.Seq)
}

// finish merges the segments and completes the job (FL-03 steps 9-11).
func (o *Orchestrator) finish(ctx context.Context, job Job) error {
	if job.Status.Terminal() {
		return nil
	}
	if moved, err := o.store.Transition(ctx, job.ID, StatusSynthesizing, StatusMerging); err != nil {
		return err
	} else if !moved && job.Status != StatusMerging {
		return nil // someone else is merging, or the job moved on
	}

	segments, err := o.store.Segments(ctx, job.ID)
	if err != nil {
		return err
	}

	keys := make([]string, 0, len(segments))
	for _, segment := range segments {
		if segment.Status != SegmentDone || segment.S3Key == "" {
			return o.failJob(ctx, job, fmt.Sprintf("segment %d is not ready to merge", segment.Seq))
		}
		keys = append(keys, segment.S3Key)
	}

	outputKey, durationMS, err := o.engine.Merge(ctx, job, keys)
	if err != nil {
		return o.failJob(ctx, job, fmt.Sprintf("merge failed: %v", err))
	}

	if _, err := o.store.Complete(ctx, job.ID, outputKey, durationMS); err != nil {
		return err
	}

	o.cleanUp(ctx, job)
	o.notify(ctx, job.ID)
	o.observeTerminal(job, StatusCompleted)

	o.logger.Info().Str("job_id", job.ID.String()).
		Int("segments", len(keys)).Int32("duration_ms", durationMS).Msg("job completed")
	return nil
}

func (o *Orchestrator) failJob(ctx context.Context, job Job, reason string) error {
	if err := o.store.Fail(ctx, job.ID, reason); err != nil {
		return err
	}

	o.cleanUp(ctx, job)
	o.notify(ctx, job.ID)
	o.observeTerminal(job, StatusFailed)

	o.logger.Error().Str("job_id", job.ID.String()).Str("reason", reason).Msg("job failed")
	return nil
}

// cleanUp removes the input text once a job is terminal (US-14 acceptance criterion 4).
// A failure here is logged, not propagated: the lifecycle rule is the backstop.
func (o *Orchestrator) cleanUp(ctx context.Context, job Job) {
	if err := o.texts.DeleteText(ctx, job.TenantID, job.ID); err != nil {
		o.logger.Warn().Err(err).Str("job_id", job.ID.String()).
			Msg("job text not deleted; lifecycle rules will remove it")
	}
}

func (o *Orchestrator) notify(ctx context.Context, jobID uuid.UUID) {
	if o.notifier == nil {
		return
	}
	job, err := o.store.ByID(ctx, jobID)
	if err != nil {
		return
	}
	o.notifier.JobFinished(ctx, job)
}

// reconcileLoop recovers work that no consumer is holding (FL-03 reconciler).
func (o *Orchestrator) reconcileLoop(ctx context.Context) {
	ticker := time.NewTicker(ReconcileInterval)
	defer ticker.Stop()

	for {
		if err := o.ReconcileOnce(ctx); err != nil && ctx.Err() == nil {
			o.logger.Error().Err(err).Msg("reconcile failed")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// ReconcileOnce re-queues stalled jobs and takes over abandoned stream entries.
func (o *Orchestrator) ReconcileOnce(ctx context.Context) error {
	stuck, err := o.store.Stuck(ctx, time.Now().Add(-StuckAfter), 100)
	if err != nil {
		return fmt.Errorf("find stuck jobs: %w", err)
	}

	for _, job := range stuck {
		// Re-queueing is safe because every step is idempotent: a job that is actually
		// progressing will simply find nothing left to do.
		if err := o.queue.Enqueue(ctx, job.ID); err != nil {
			return fmt.Errorf("requeue job %s: %w", job.ID, err)
		}
		o.logger.Warn().Str("job_id", job.ID.String()).Str("status", string(job.Status)).
			Msg("stalled job re-queued")
	}

	for _, stream := range []string{StreamJobs, StreamSegments} {
		entries, err := o.queue.ClaimStale(ctx, stream, o.consumer, 5*time.Minute, claimBatch)
		if err != nil {
			return fmt.Errorf("claim stale on %s: %w", stream, err)
		}
		for _, entry := range entries {
			handle := o.handleJob
			if stream == StreamSegments {
				handle = o.handleSegment
			}
			if err := handle(ctx, entry); err != nil && ctx.Err() == nil {
				o.logger.Error().Err(err).Str("stream", stream).Msg("recovered item failed")
			}
			if err := o.queue.Ack(ctx, stream, entry.ID); err != nil && ctx.Err() == nil {
				o.logger.Warn().Err(err).Msg("ack of recovered item failed")
			}
		}
	}

	return nil
}

func findSegment(segments []Segment, seq int32) (Segment, bool) {
	for _, segment := range segments {
		if segment.Seq == seq {
			return segment, true
		}
	}
	return Segment{}, false
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// segmentHash is the cache key for one segment, so a phrase repeated across jobs is
// synthesized once (ADR-006). It is the same derivation the synthesis path uses, which
// is what lets a job hit audio a sync request produced earlier.
func segmentHash(job Job, text string) []byte {
	key := cache.Derive(cache.Input{
		ModelVersion: job.Metadata[modelVersionKey],
		VoiceID:      job.VoiceID,
		Normalize:    job.Normalize,
		Params:       job.Params,
		Text:         text,
	})
	return key.Bytes()
}

// modelVersionKey is where the orchestrator records which model segmented a job, so a
// mid-job model upgrade does not silently mix two voices' audio.
const modelVersionKey = "_model_version"
