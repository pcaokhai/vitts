package jobs_test

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/pcaokhai/vitts/gateway/internal/jobs"
)

// memRepo is an in-memory Repository with the unique index the schema enforces.
type memRepo struct {
	mu      sync.Mutex
	byID    map[uuid.UUID]jobs.Job
	byIdem  map[string]uuid.UUID
	creates int
}

func newMemRepo() *memRepo {
	return &memRepo{byID: map[uuid.UUID]jobs.Job{}, byIdem: map[string]uuid.UUID{}}
}

func idemKey(tenantID uuid.UUID, key string) string { return tenantID.String() + "|" + key }

func (m *memRepo) Create(_ context.Context, job jobs.Job) (jobs.Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.creates++
	key := idemKey(job.TenantID, job.IdempotencyKey)
	if _, exists := m.byIdem[key]; exists {
		return jobs.Job{}, jobs.ErrIdempotencyConflict // stands in for the unique violation
	}
	job.CreatedAt = time.Now().UTC()
	m.byID[job.ID], m.byIdem[key] = job, job.ID
	return job, nil
}

func (m *memRepo) ByIdempotencyKey(_ context.Context, tenantID uuid.UUID, key string) (jobs.Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	id, ok := m.byIdem[idemKey(tenantID, key)]
	if !ok {
		return jobs.Job{}, jobs.ErrNotFound
	}
	return m.byID[id], nil
}

func (m *memRepo) Get(_ context.Context, tenantID, jobID uuid.UUID) (jobs.Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	job, ok := m.byID[jobID]
	if !ok || job.TenantID != tenantID {
		return jobs.Job{}, jobs.ErrNotFound
	}
	return job, nil
}

func (m *memRepo) List(_ context.Context, tenantID uuid.UUID, _ *time.Time, limit int32) ([]jobs.Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var out []jobs.Job
	for _, job := range m.byID {
		if job.TenantID == tenantID && int32(len(out)) < limit {
			out = append(out, job)
		}
	}
	return out, nil
}

func (m *memRepo) Cancel(_ context.Context, tenantID, jobID uuid.UUID) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	job, ok := m.byID[jobID]
	if !ok || job.TenantID != tenantID || job.Status.Terminal() {
		return false, nil
	}
	job.Status = jobs.StatusCancelled
	m.byID[jobID] = job
	return true, nil
}

type memQueue struct {
	mu       sync.Mutex
	enqueued []uuid.UUID
	err      error
}

func (q *memQueue) Enqueue(_ context.Context, jobID uuid.UUID) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.err != nil {
		return q.err
	}
	q.enqueued = append(q.enqueued, jobID)
	return nil
}

type memTexts struct {
	mu    sync.Mutex
	texts map[uuid.UUID]string
}

func (t *memTexts) PutText(_ context.Context, _, jobID uuid.UUID, text string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.texts == nil {
		t.texts = map[uuid.UUID]string{}
	}
	t.texts[jobID] = text
	return nil
}

func (t *memTexts) SignedOutputURL(_ context.Context, key string, _ time.Duration) (string, error) {
	return "https://storage.example/" + key, nil
}

type allowAllGuard struct{}

func (allowAllGuard) Check(context.Context, string) error { return nil }

func newService(t *testing.T) (*jobs.Service, *memRepo, *memQueue, *memTexts) {
	t.Helper()

	repo, queue, texts := newMemRepo(), &memQueue{}, &memTexts{}
	return jobs.NewService(repo, queue, texts, allowAllGuard{}, zerolog.New(io.Discard)),
		repo, queue, texts
}

func request(tenantID uuid.UUID) jobs.Request {
	return jobs.Request{
		TenantID: tenantID, KeyID: uuid.New(), IdempotencyKey: "idem-1",
		Text: "Xin chào các bạn, đây là một công việc dài.", VoiceID: "maichi",
		Normalize: true,
	}
}

func TestCreateStoresTextQueuesAndReturnsQueued(t *testing.T) {
	t.Parallel()

	service, _, queue, texts := newService(t)
	tenantID := uuid.New()

	created, err := service.Create(context.Background(), request(tenantID), 100_000)

	require.NoError(t, err)
	require.False(t, created.Existed)
	require.Equal(t, jobs.StatusQueued, created.Job.Status)
	require.Equal(t, "mp3", created.Job.Format, "the contract's job default")
	require.Len(t, queue.enqueued, 1)
	require.Contains(t, texts.texts, created.Job.ID, "text is in object storage, never Postgres")
}

// T-15: the same key and body returns the same job; a different body is a conflict.
func TestIdempotency(t *testing.T) {
	t.Parallel()

	service, repo, _, _ := newService(t)
	tenantID := uuid.New()
	req := request(tenantID)

	first, err := service.Create(context.Background(), req, 100_000)
	require.NoError(t, err)

	second, err := service.Create(context.Background(), req, 100_000)
	require.NoError(t, err)
	require.True(t, second.Existed)
	require.Equal(t, first.Job.ID, second.Job.ID, "a retry must return the original job")
	require.Equal(t, 1, repo.creates, "and must not create another")

	different := req
	different.Text = "Một nội dung hoàn toàn khác."
	_, err = service.Create(context.Background(), different, 100_000)

	require.ErrorIs(t, err, jobs.ErrIdempotencyConflict)
}

// Reformatting a body must not look like a different job.
func TestIdempotencyIgnoresWhitespaceDifferences(t *testing.T) {
	t.Parallel()

	service, _, _, _ := newService(t)
	tenantID := uuid.New()
	req := request(tenantID)

	first, err := service.Create(context.Background(), req, 100_000)
	require.NoError(t, err)

	reformatted := req
	reformatted.Text = "  Xin chào các bạn,   đây là một công việc dài.  "
	second, err := service.Create(context.Background(), reformatted, 100_000)

	require.NoError(t, err)
	require.Equal(t, first.Job.ID, second.Job.ID)
}

func TestKeysAreScopedToTheTenant(t *testing.T) {
	t.Parallel()

	service, _, _, _ := newService(t)
	a, b := uuid.New(), uuid.New()

	first, err := service.Create(context.Background(), request(a), 100_000)
	require.NoError(t, err)
	second, err := service.Create(context.Background(), request(b), 100_000)

	require.NoError(t, err)
	require.NotEqual(t, first.Job.ID, second.Job.ID,
		"two tenants may reuse the same idempotency key")
}

// T-12: another tenant's job does not exist as far as this tenant is concerned.
func TestAnotherTenantsJobIsNotFound(t *testing.T) {
	t.Parallel()

	service, _, _, _ := newService(t)
	owner, other := uuid.New(), uuid.New()
	created, err := service.Create(context.Background(), request(owner), 100_000)
	require.NoError(t, err)

	_, err = service.Get(context.Background(), other, created.Job.ID)
	require.ErrorIs(t, err, jobs.ErrNotFound)

	_, err = service.Cancel(context.Background(), other, created.Job.ID)
	require.ErrorIs(t, err, jobs.ErrNotFound)

	listed, err := service.List(context.Background(), other, nil, 50)
	require.NoError(t, err)
	require.Empty(t, listed)
}

func TestCancelIsIdempotent(t *testing.T) {
	t.Parallel()

	service, _, _, _ := newService(t)
	tenantID := uuid.New()
	created, err := service.Create(context.Background(), request(tenantID), 100_000)
	require.NoError(t, err)

	first, err := service.Cancel(context.Background(), tenantID, created.Job.ID)
	require.NoError(t, err)
	require.Equal(t, jobs.StatusCancelled, first.Status)

	second, err := service.Cancel(context.Background(), tenantID, created.Job.ID)

	require.NoError(t, err, "cancelling twice must not fail a client that retried")
	require.Equal(t, jobs.StatusCancelled, second.Status)
}

func TestPlanLimitIsEnforced(t *testing.T) {
	t.Parallel()

	service, _, _, _ := newService(t)
	req := request(uuid.New())

	_, err := service.Create(context.Background(), req, 10)

	require.ErrorIs(t, err, jobs.ErrPlanLimit)
}

func TestMissingIdempotencyKeyIsRefused(t *testing.T) {
	t.Parallel()

	service, _, _, _ := newService(t)
	req := request(uuid.New())
	req.IdempotencyKey = ""

	_, err := service.Create(context.Background(), req, 100_000)

	require.ErrorIs(t, err, jobs.ErrInvalidRequest)
}

// A queue failure must not lose the job: the row exists and the reconciler re-queues it.
func TestJobSurvivesAQueueFailure(t *testing.T) {
	t.Parallel()

	repo, texts := newMemRepo(), &memTexts{}
	queue := &memQueue{err: context.DeadlineExceeded}
	service := jobs.NewService(repo, queue, texts, allowAllGuard{}, zerolog.New(io.Discard))

	created, err := service.Create(context.Background(), request(uuid.New()), 100_000)

	require.NoError(t, err)
	require.Equal(t, jobs.StatusQueued, created.Job.Status)
	require.Len(t, repo.byID, 1)
}

func TestInternalFingerprintIsNotExposed(t *testing.T) {
	t.Parallel()

	service, _, _, _ := newService(t)
	req := request(uuid.New())
	req.Metadata = map[string]string{"campaign": "spring"}

	created, err := service.Create(context.Background(), req, 100_000)
	require.NoError(t, err)

	public := jobs.PublicMetadata(created.Job.Metadata)

	require.Equal(t, map[string]string{"campaign": "spring"}, public)
}
