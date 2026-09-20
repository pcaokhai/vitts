package maintenance_test

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/pcaokhai/vitts/gateway/internal/maintenance"
)

// sharedLock behaves like the Redis lock across several schedulers: one holder at a time.
type sharedLock struct {
	mu   sync.Mutex
	held map[string]bool
	err  error
}

func newSharedLock() *sharedLock { return &sharedLock{held: map[string]bool{}} }

func (l *sharedLock) Acquire(_ context.Context, name string, _ time.Duration) (func(), bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.err != nil {
		return nil, false, l.err
	}
	if l.held[name] {
		return func() {}, false, nil
	}
	l.held[name] = true
	return func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		l.held[name] = false
	}, true, nil
}

// T-19: two replicas on the same schedule must produce one execution.
func TestOnlyOneReplicaRunsATask(t *testing.T) {
	t.Parallel()

	lock := newSharedLock()
	var runs atomic.Int64
	release := make(chan struct{})

	task := maintenance.Task{
		Name: "rollup", Every: time.Hour, LockTTL: time.Minute,
		Run: func(context.Context) error {
			runs.Add(1)
			<-release // hold the lock while the other replica tries
			return nil
		},
	}

	replicaA := maintenance.New(lock, zerolog.New(io.Discard))
	replicaB := maintenance.New(lock, zerolog.New(io.Discard))

	started := make(chan struct{})
	go func() {
		close(started)
		replicaA.RunOnce(context.Background(), task)
	}()
	<-started
	require.Eventually(t, func() bool { return runs.Load() == 1 }, time.Second, 5*time.Millisecond)

	replicaB.RunOnce(context.Background(), task)

	require.Equal(t, int64(1), runs.Load(), "the second replica must skip, not queue")
	close(release)
}

func TestTheLockIsReleasedForTheNextRun(t *testing.T) {
	t.Parallel()

	lock := newSharedLock()
	var runs atomic.Int64
	task := maintenance.Task{
		Name: "rollup", Every: time.Hour,
		Run: func(context.Context) error { runs.Add(1); return nil },
	}
	scheduler := maintenance.New(lock, zerolog.New(io.Discard))

	scheduler.RunOnce(context.Background(), task)
	scheduler.RunOnce(context.Background(), task)

	require.Equal(t, int64(2), runs.Load())
}

func TestAFailingTaskStillReleasesTheLock(t *testing.T) {
	t.Parallel()

	lock := newSharedLock()
	var runs atomic.Int64
	task := maintenance.Task{
		Name: "rollup", Every: time.Hour,
		Run: func(context.Context) error {
			runs.Add(1)
			return errors.New("database down")
		},
	}
	scheduler := maintenance.New(lock, zerolog.New(io.Discard))

	scheduler.RunOnce(context.Background(), task)
	scheduler.RunOnce(context.Background(), task)

	require.Equal(t, int64(2), runs.Load(), "a failure must not wedge the schedule")
}

func TestALockFailureSkipsTheRun(t *testing.T) {
	t.Parallel()

	lock := newSharedLock()
	lock.err = errors.New("redis down")
	var runs atomic.Int64

	maintenance.New(lock, zerolog.New(io.Discard)).RunOnce(context.Background(), maintenance.Task{
		Name: "rollup", Every: time.Hour,
		Run: func(context.Context) error { runs.Add(1); return nil },
	})

	require.Zero(t, runs.Load(), "without a lock we cannot know we are alone")
}

// evictionStore is the database side of a cache sweep.
type evictionStore struct {
	mu      sync.Mutex
	entries []maintenance.CacheEntry
	deleted [][]byte
}

func (s *evictionStore) RollupUsage(context.Context, time.Time, time.Time) error { return nil }

func (s *evictionStore) EvictableCacheEntries(_ context.Context, _ time.Time, _ int32) ([]maintenance.CacheEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.entries, nil
}

func (s *evictionStore) DeleteCacheEntry(_ context.Context, key []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deleted = append(s.deleted, key)
	return nil
}

func (s *evictionStore) EnsurePartition(context.Context, time.Time) (string, error) {
	return "synth_requests_209901", nil
}

type objectStore struct {
	mu      sync.Mutex
	deleted []string
	failFor map[string]bool
}

func (o *objectStore) Delete(_ context.Context, key string) error {
	o.mu.Lock()
	defer o.mu.Unlock()

	if o.failFor[key] {
		return errors.New("object store unavailable")
	}
	o.deleted = append(o.deleted, key)
	return nil
}

func findTask(t *testing.T, tasks []maintenance.Task, name string) maintenance.Task {
	t.Helper()
	for _, task := range tasks {
		if task.Name == name {
			return task
		}
	}
	t.Fatalf("task %q not found", name)
	return maintenance.Task{}
}

func TestEvictionRemovesObjectThenRow(t *testing.T) {
	t.Parallel()

	store := &evictionStore{entries: []maintenance.CacheEntry{
		{CacheKey: []byte("a"), S3Key: "cache/aa/a.pcm"},
		{CacheKey: []byte("b"), S3Key: "cache/bb/b.pcm"},
	}}
	objects := &objectStore{failFor: map[string]bool{}}
	tasks := maintenance.Tasks(store, objects, zerolog.New(io.Discard))

	require.NoError(t, findTask(t, tasks, "cache-eviction").Run(context.Background()))

	require.Equal(t, []string{"cache/aa/a.pcm", "cache/bb/b.pcm"}, objects.deleted)
	require.Len(t, store.deleted, 2)
}

// A row whose object could not be deleted must stay, so the next sweep retries it:
// deleting the row first would orphan the object forever.
func TestEvictionKeepsTheRowWhenTheObjectSurvives(t *testing.T) {
	t.Parallel()

	store := &evictionStore{entries: []maintenance.CacheEntry{
		{CacheKey: []byte("a"), S3Key: "cache/aa/a.pcm"},
		{CacheKey: []byte("b"), S3Key: "cache/bb/b.pcm"},
	}}
	objects := &objectStore{failFor: map[string]bool{"cache/aa/a.pcm": true}}
	tasks := maintenance.Tasks(store, objects, zerolog.New(io.Discard))

	require.NoError(t, findTask(t, tasks, "cache-eviction").Run(context.Background()))

	require.Len(t, store.deleted, 1)
	require.Equal(t, []byte("b"), store.deleted[0])
}

func TestPartitionMaintenanceLooksAhead(t *testing.T) {
	t.Parallel()

	store := &evictionStore{}
	tasks := maintenance.Tasks(store, &objectStore{}, zerolog.New(io.Discard))

	require.NoError(t, findTask(t, tasks, "partition-maintenance").Run(context.Background()))
}

func TestScheduleMatchesTheFlowDoc(t *testing.T) {
	t.Parallel()

	tasks := maintenance.Tasks(&evictionStore{}, &objectStore{}, zerolog.New(io.Discard))

	require.Equal(t, 5*time.Minute, findTask(t, tasks, "usage-rollup").Every)
	require.Equal(t, 24*time.Hour, findTask(t, tasks, "cache-eviction").Every)
	require.Greater(t, maintenance.RollupWindow, maintenance.RollupEvery,
		"the rollup window must overlap its interval so a missed tick is still covered")
}
