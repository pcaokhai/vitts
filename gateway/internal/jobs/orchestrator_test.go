package jobs_test

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/pcaokhai/vitts/gateway/internal/jobs"
)

// stateStore is an in-memory StateStore with the conditional-update semantics the real
// schema enforces, so the state machine is tested against the rules it actually runs on.
type stateStore struct {
	mu       sync.Mutex
	jobs     map[uuid.UUID]jobs.Job
	segments map[uuid.UUID][]jobs.Segment
}

func newStateStore() *stateStore {
	return &stateStore{jobs: map[uuid.UUID]jobs.Job{}, segments: map[uuid.UUID][]jobs.Segment{}}
}

func (s *stateStore) put(job jobs.Job) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.jobs[job.ID] = job
}

func (s *stateStore) ByID(_ context.Context, jobID uuid.UUID) (jobs.Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[jobID]
	if !ok {
		return jobs.Job{}, jobs.ErrNotFound
	}
	return job, nil
}

func (s *stateStore) Get(ctx context.Context, _, jobID uuid.UUID) (jobs.Job, error) {
	return s.ByID(ctx, jobID)
}

func (s *stateStore) Transition(_ context.Context, jobID uuid.UUID, from, to jobs.Status) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	job, ok := s.jobs[jobID]
	if !ok || job.Status != from {
		return false, nil
	}
	job.Status = to
	s.jobs[jobID] = job
	return true, nil
}

func (s *stateStore) SetSegmentsTotal(_ context.Context, jobID uuid.UUID, total int32) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	job, ok := s.jobs[jobID]
	if !ok || job.Status != jobs.StatusSegmenting {
		return false, nil
	}
	job.SegmentsTotal, job.Status = &total, jobs.StatusSynthesizing
	s.jobs[jobID] = job
	return true, nil
}

func (s *stateStore) CreateSegment(_ context.Context, jobID uuid.UUID, seq int32, hash []byte, chars int32) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, existing := range s.segments[jobID] {
		if existing.Seq == seq {
			return nil // insert ... on conflict do nothing
		}
	}
	s.segments[jobID] = append(s.segments[jobID], jobs.Segment{
		JobID: jobID, Seq: seq, TextHash: hash, Chars: chars, Status: jobs.SegmentPending,
	})
	return nil
}

func (s *stateStore) Segments(_ context.Context, jobID uuid.UUID) ([]jobs.Segment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]jobs.Segment, len(s.segments[jobID]))
	copy(out, s.segments[jobID])
	return out, nil
}

func (s *stateStore) ClaimSegment(_ context.Context, jobID uuid.UUID, seq int32) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i, segment := range s.segments[jobID] {
		if segment.Seq != seq {
			continue
		}
		if segment.Status != jobs.SegmentPending && segment.Status != jobs.SegmentFailed {
			return false, nil
		}
		s.segments[jobID][i].Status = jobs.SegmentRunning
		s.segments[jobID][i].Attempts++
		return true, nil
	}
	return false, nil
}

func (s *stateStore) CompleteSegment(_ context.Context, jobID uuid.UUID, seq int32, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i, segment := range s.segments[jobID] {
		if segment.Seq == seq && segment.Status == jobs.SegmentRunning {
			s.segments[jobID][i].Status, s.segments[jobID][i].S3Key = jobs.SegmentDone, key
			job := s.jobs[jobID]
			job.SegmentsDone++
			s.jobs[jobID] = job
		}
	}
	return nil
}

func (s *stateStore) FailSegment(_ context.Context, jobID uuid.UUID, seq int32, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i, segment := range s.segments[jobID] {
		if segment.Seq == seq {
			s.segments[jobID][i].Status, s.segments[jobID][i].LastError = jobs.SegmentFailed, reason
		}
	}
	return nil
}

func (s *stateStore) UnfinishedSegments(_ context.Context, jobID uuid.UUID) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var n int64
	for _, segment := range s.segments[jobID] {
		if segment.Status != jobs.SegmentDone {
			n++
		}
	}
	return n, nil
}

func (s *stateStore) Fail(_ context.Context, jobID uuid.UUID, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	job := s.jobs[jobID]
	if job.Status.Terminal() {
		return nil
	}
	job.Status, job.Error = jobs.StatusFailed, reason
	s.jobs[jobID] = job
	return nil
}

func (s *stateStore) Complete(_ context.Context, jobID uuid.UUID, key string, durationMS int32) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	job := s.jobs[jobID]
	if job.Status != jobs.StatusMerging {
		return false, nil
	}
	job.Status, job.OutputS3Key, job.DurationMS = jobs.StatusCompleted, key, &durationMS
	s.jobs[jobID] = job
	return true, nil
}

func (s *stateStore) Stuck(_ context.Context, _ time.Time, _ int32) ([]jobs.Job, error) {
	return nil, nil
}

// recordingQueue records what the orchestrator enqueues and replays it on demand.
type recordingQueue struct {
	mu       sync.Mutex
	segments []jobs.QueueEntry
	jobsSeen []uuid.UUID
	nextID   int
}

func (q *recordingQueue) Enqueue(_ context.Context, jobID uuid.UUID) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.jobsSeen = append(q.jobsSeen, jobID)
	return nil
}

func (q *recordingQueue) EnqueueSegment(_ context.Context, jobID uuid.UUID, seq int32) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.nextID++
	q.segments = append(q.segments, jobs.QueueEntry{
		ID: string(rune('a' + q.nextID)), JobID: jobID, Seq: seq, Stream: jobs.StreamSegments,
	})
	return nil
}

func (q *recordingQueue) Claim(context.Context, string, string, int64, time.Duration) ([]jobs.QueueEntry, error) {
	return nil, nil
}

func (q *recordingQueue) ClaimStale(context.Context, string, string, time.Duration, int64) ([]jobs.QueueEntry, error) {
	return nil, nil
}

func (q *recordingQueue) Ack(context.Context, string, string) error { return nil }

func (q *recordingQueue) takeSegments() []jobs.QueueEntry {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := q.segments
	q.segments = nil
	return out
}

type fixedTexts struct {
	text    string
	err     error
	deletes int
	mu      sync.Mutex
}

func (t *fixedTexts) GetText(context.Context, uuid.UUID, uuid.UUID) (string, error) {
	if t.err != nil {
		return "", t.err
	}
	return t.text, nil
}

func (t *fixedTexts) DeleteText(context.Context, uuid.UUID, uuid.UUID) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.deletes++
	return nil
}

// scriptedEngine counts model work so a test can prove work was not repeated.
type scriptedEngine struct {
	mu          sync.Mutex
	pieces      []string
	synthesized []int32
	merges      int
	failSeq     map[int32]int // seq -> how many times it should still fail
	mergeErr    error
}

func (e *scriptedEngine) Split(context.Context, jobs.Job, string) ([]string, error) {
	return e.pieces, nil
}

func (e *scriptedEngine) Synthesize(_ context.Context, _ jobs.Job, seq int32, _ string) (jobs.SegmentResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if remaining, ok := e.failSeq[seq]; ok && remaining > 0 {
		e.failSeq[seq] = remaining - 1
		return jobs.SegmentResult{}, errors.New("worker unavailable")
	}

	e.synthesized = append(e.synthesized, seq)
	return jobs.SegmentResult{S3Key: "seg/" + string(rune('0'+seq)) + ".pcm", DurationMS: 1000}, nil
}

func (e *scriptedEngine) Merge(context.Context, jobs.Job, []string) (string, int32, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.mergeErr != nil {
		return "", 0, e.mergeErr
	}
	e.merges++
	return "jobs/out.mp3", 3000, nil
}

func (e *scriptedEngine) counts() (synth int, merges int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.synthesized), e.merges
}

type orchestratorFixture struct {
	orchestrator *jobs.Orchestrator
	store        *stateStore
	queue        *recordingQueue
	engine       *scriptedEngine
	texts        *fixedTexts
	job          jobs.Job
}

func newOrchestrator(t *testing.T, pieces int) orchestratorFixture {
	t.Helper()

	store, queue := newStateStore(), &recordingQueue{}
	texts := &fixedTexts{text: "Câu một. Câu hai. Câu ba."}

	segments := make([]string, pieces)
	for i := range segments {
		segments[i] = "Câu " + string(rune('0'+i)) + "."
	}
	engine := &scriptedEngine{pieces: segments, failSeq: map[int32]int{}}

	job := jobs.Job{
		ID: uuid.New(), TenantID: uuid.New(), Status: jobs.StatusQueued,
		VoiceID: "maichi", Format: "mp3", SampleRate: 48000, Metadata: map[string]string{},
	}
	store.put(job)

	orchestrator := jobs.NewOrchestrator(queue, store, texts, engine, nil, "test", zerolog.New(io.Discard))
	orchestrator.SetSleepForTest(func(context.Context, time.Duration) error { return nil })

	return orchestratorFixture{orchestrator, store, queue, engine, texts, job}
}

// drain processes queued segments until none remain, the way the consumer loop would.
func (f orchestratorFixture) drain(t *testing.T) {
	t.Helper()

	for range 50 {
		entries := f.queue.takeSegments()
		if len(entries) == 0 {
			return
		}
		for _, entry := range entries {
			require.NoError(t, f.orchestrator.HandleSegmentForTest(context.Background(), entry))
		}
	}
	t.Fatal("segments never drained")
}

func TestJobRunsToCompletion(t *testing.T) {
	t.Parallel()

	f := newOrchestrator(t, 3)

	require.NoError(t, f.orchestrator.HandleJobForTest(context.Background(),
		jobs.QueueEntry{JobID: f.job.ID, Stream: jobs.StreamJobs}))
	f.drain(t)

	final, err := f.store.ByID(context.Background(), f.job.ID)
	require.NoError(t, err)
	require.Equal(t, jobs.StatusCompleted, final.Status)
	require.Equal(t, "jobs/out.mp3", final.OutputS3Key)
	require.EqualValues(t, 3, *final.SegmentsTotal)
	require.EqualValues(t, 3, final.SegmentsDone)

	synthesized, merges := f.engine.counts()
	require.Equal(t, 3, synthesized)
	require.Equal(t, 1, merges)
	require.Positive(t, f.texts.deletes, "input text is removed once the job is terminal")
}

// T-108. A job whose orchestrator died between "queued -> segmenting" and the segment
// rows must resume. It used to be re-queued by the reconciler every minute and dropped
// every time, because the orchestrator read the status it had set itself as proof that
// someone else owned the job (docs/reports/drill-m3.md, finding 6).
func TestAJobStuckInSegmentingResumes(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	f := newOrchestrator(t, 3)

	moved, err := f.store.Transition(ctx, f.job.ID, jobs.StatusQueued, jobs.StatusSegmenting)
	require.NoError(t, err)
	require.True(t, moved)

	require.NoError(t, f.orchestrator.HandleJobForTest(ctx,
		jobs.QueueEntry{JobID: f.job.ID, Stream: jobs.StreamJobs}))
	f.drain(t)

	final, err := f.store.ByID(ctx, f.job.ID)
	require.NoError(t, err)
	require.Equal(t, jobs.StatusCompleted, final.Status)
	require.EqualValues(t, 3, final.SegmentsDone)
}

// T-09: replaying work after a crash must not re-synthesize finished segments.
func TestReplayDoesNotRepeatFinishedSegments(t *testing.T) {
	t.Parallel()

	f := newOrchestrator(t, 3)
	ctx := context.Background()

	require.NoError(t, f.orchestrator.HandleJobForTest(ctx, jobs.QueueEntry{JobID: f.job.ID}))
	first := f.queue.takeSegments()
	require.Len(t, first, 3)

	// Two segments finish, then the orchestrator "crashes" before the third.
	for _, entry := range first[:2] {
		require.NoError(t, f.orchestrator.HandleSegmentForTest(ctx, entry))
	}
	synthesizedBefore, _ := f.engine.counts()
	require.Equal(t, 2, synthesizedBefore)

	// A new orchestrator replays the job entry and every segment entry.
	require.NoError(t, f.orchestrator.HandleJobForTest(ctx, jobs.QueueEntry{JobID: f.job.ID}))
	for _, entry := range first {
		require.NoError(t, f.orchestrator.HandleSegmentForTest(ctx, entry))
	}
	f.drain(t)

	synthesizedAfter, merges := f.engine.counts()
	require.Equal(t, 3, synthesizedAfter, "each segment is synthesized exactly once")
	require.Equal(t, 1, merges)

	final, err := f.store.ByID(ctx, f.job.ID)
	require.NoError(t, err)
	require.Equal(t, jobs.StatusCompleted, final.Status)
}

// US-14 acceptance criterion 3.
func TestSegmentRetriesThenFailsTheJob(t *testing.T) {
	t.Parallel()

	f := newOrchestrator(t, 2)
	f.engine.failSeq[1] = 10 // always fails
	ctx := context.Background()

	require.NoError(t, f.orchestrator.HandleJobForTest(ctx, jobs.QueueEntry{JobID: f.job.ID}))
	for range 10 {
		entries := f.queue.takeSegments()
		if len(entries) == 0 {
			break
		}
		for _, entry := range entries {
			require.NoError(t, f.orchestrator.HandleSegmentForTest(ctx, entry))
		}
	}

	final, err := f.store.ByID(ctx, f.job.ID)
	require.NoError(t, err)
	require.Equal(t, jobs.StatusFailed, final.Status)
	require.Contains(t, final.Error, "after 3 attempts")

	segments, err := f.store.Segments(ctx, f.job.ID)
	require.NoError(t, err)
	for _, segment := range segments {
		if segment.Seq == 1 {
			require.EqualValues(t, jobs.MaxSegmentAttempts, segment.Attempts)
		}
	}
}

func TestATransientFailureRecovers(t *testing.T) {
	t.Parallel()

	f := newOrchestrator(t, 2)
	f.engine.failSeq[0] = 1 // fails once, then succeeds
	ctx := context.Background()

	require.NoError(t, f.orchestrator.HandleJobForTest(ctx, jobs.QueueEntry{JobID: f.job.ID}))
	f.drain(t)

	final, err := f.store.ByID(ctx, f.job.ID)
	require.NoError(t, err)
	require.Equal(t, jobs.StatusCompleted, final.Status)
}

// US-14 acceptance criterion 5.
func TestCancelledJobDispatchesNoMoreSegments(t *testing.T) {
	t.Parallel()

	f := newOrchestrator(t, 3)
	ctx := context.Background()
	require.NoError(t, f.orchestrator.HandleJobForTest(ctx, jobs.QueueEntry{JobID: f.job.ID}))
	entries := f.queue.takeSegments()

	cancelled := f.job
	cancelled.Status = jobs.StatusCancelled
	f.store.put(cancelled)

	for _, entry := range entries {
		require.NoError(t, f.orchestrator.HandleSegmentForTest(ctx, entry))
	}

	synthesized, merges := f.engine.counts()
	require.Zero(t, synthesized, "a cancelled job must not reach the model")
	require.Zero(t, merges)
}

func TestMergeFailureFailsTheJob(t *testing.T) {
	t.Parallel()

	f := newOrchestrator(t, 2)
	f.engine.mergeErr = errors.New("worker refused")
	ctx := context.Background()

	require.NoError(t, f.orchestrator.HandleJobForTest(ctx, jobs.QueueEntry{JobID: f.job.ID}))
	f.drain(t)

	final, err := f.store.ByID(ctx, f.job.ID)
	require.NoError(t, err)
	require.Equal(t, jobs.StatusFailed, final.Status)
	require.Contains(t, final.Error, "merge failed")
}

func TestUnreadableInputFailsTheJob(t *testing.T) {
	t.Parallel()

	f := newOrchestrator(t, 2)
	f.texts.err = errors.New("object missing")

	require.NoError(t, f.orchestrator.HandleJobForTest(context.Background(),
		jobs.QueueEntry{JobID: f.job.ID}))

	final, err := f.store.ByID(context.Background(), f.job.ID)
	require.NoError(t, err)
	require.Equal(t, jobs.StatusFailed, final.Status)
	require.Contains(t, final.Error, "input text unavailable")
}
