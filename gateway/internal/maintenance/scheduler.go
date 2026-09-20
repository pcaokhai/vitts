// Package maintenance runs the scheduled work in FL-07.
//
// Every gateway replica runs the same schedule, so each job takes a lock first: the work
// is idempotent, but running the eviction sweep on three replicas at once would triple
// the object-store traffic for no benefit.
package maintenance

import (
	"context"
	"fmt"
	"time"

	"github.com/rs/zerolog"
)

// Lock is a short-lived exclusive lease across replicas.
type Lock interface {
	// Acquire returns false when another replica holds the lock, which is a normal
	// outcome rather than an error.
	Acquire(ctx context.Context, name string, ttl time.Duration) (release func(), ok bool, err error)
}

// Task is one scheduled unit of work.
type Task struct {
	Name  string
	Every time.Duration
	Run   func(ctx context.Context) error
	// LockTTL bounds how long a crashed replica can block the task. It must exceed the
	// task's worst-case run time, or two replicas could overlap.
	LockTTL time.Duration
}

// Scheduler runs tasks on their interval, one replica at a time.
type Scheduler struct {
	lock   Lock
	logger zerolog.Logger
	tasks  []Task
}

// New wires the scheduler.
func New(lock Lock, logger zerolog.Logger, tasks ...Task) *Scheduler {
	return &Scheduler{lock: lock, logger: logger, tasks: tasks}
}

// Run starts every task until the context is cancelled.
func (s *Scheduler) Run(ctx context.Context) {
	for _, task := range s.tasks {
		go s.loop(ctx, task)
	}
}

func (s *Scheduler) loop(ctx context.Context, task Task) {
	ticker := time.NewTicker(task.Every)
	defer ticker.Stop()

	for {
		s.RunOnce(ctx, task)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// RunOnce takes the lock and runs the task, skipping quietly when another replica has it.
func (s *Scheduler) RunOnce(ctx context.Context, task Task) {
	ttl := task.LockTTL
	if ttl <= 0 {
		ttl = task.Every
	}

	release, ok, err := s.lock.Acquire(ctx, "sched:"+task.Name, ttl)
	if err != nil {
		s.logger.Error().Err(err).Str("task", task.Name).Msg("scheduler lock failed")
		return
	}
	if !ok {
		return // another replica is running it, which is the point of the lock
	}
	defer release()

	started := time.Now()
	if err := task.Run(ctx); err != nil {
		if ctx.Err() != nil {
			return // shutting down
		}
		s.logger.Error().Err(err).Str("task", task.Name).Msg("scheduled task failed")
		return
	}

	s.logger.Info().Str("task", task.Name).
		Dur("took", time.Since(started)).Msg("scheduled task complete")
}

// Schedules from FL-07.
const (
	RollupEvery     = 5 * time.Minute
	EvictionEvery   = 24 * time.Hour
	PartitionsEvery = 24 * time.Hour
)

// RollupWindow is how far back the usage rollup recomputes on each run.
//
// Wider than the interval on purpose: a row inserted moments before a run, or a replica
// that missed a tick, is still picked up by the next one because the aggregate is
// recomputed rather than incremented (FL-07).
const RollupWindow = 30 * time.Minute

// CacheMaxIdle is how long a cache entry may go unused before eviction (NFR-11).
const CacheMaxIdle = 30 * 24 * time.Hour

// Store is the database side of maintenance.
type Store interface {
	RollupUsage(ctx context.Context, from, to time.Time) error
	EvictableCacheEntries(ctx context.Context, idleBefore time.Time, limit int32) ([]CacheEntry, error)
	DeleteCacheEntry(ctx context.Context, cacheKey []byte) error
	EnsurePartition(ctx context.Context, at time.Time) (string, error)
}

// CacheEntry is one evictable cached object.
type CacheEntry struct {
	CacheKey []byte
	S3Key    string
}

// Objects deletes evicted audio.
type Objects interface {
	Delete(ctx context.Context, key string) error
}

// evictionBatch bounds one sweep, so a large backlog is worked through over several
// runs rather than in one long transaction.
const evictionBatch int32 = 500

// Tasks builds the standard schedule.
func Tasks(store Store, objects Objects, logger zerolog.Logger) []Task {
	return []Task{
		{
			Name:    "usage-rollup",
			Every:   RollupEvery,
			LockTTL: RollupEvery,
			Run: func(ctx context.Context) error {
				now := time.Now().UTC()
				return store.RollupUsage(ctx, now.Add(-RollupWindow), now.Add(time.Minute))
			},
		},
		{
			Name:    "cache-eviction",
			Every:   EvictionEvery,
			LockTTL: time.Hour,
			Run: func(ctx context.Context) error {
				return evictCache(ctx, store, objects, logger)
			},
		},
		{
			Name:    "partition-maintenance",
			Every:   PartitionsEvery,
			LockTTL: 10 * time.Minute,
			Run: func(ctx context.Context) error {
				// Two months ahead, so a missed run cannot leave metering with nowhere
				// to write (migration 0001 defines the function).
				for _, at := range []time.Time{
					time.Now().UTC().AddDate(0, 1, 0),
					time.Now().UTC().AddDate(0, 2, 0),
				} {
					name, err := store.EnsurePartition(ctx, at)
					if err != nil {
						return fmt.Errorf("ensure partition for %s: %w", at.Format("2006-01"), err)
					}
					logger.Debug().Str("partition", name).Msg("partition ensured")
				}
				return nil
			},
		},
	}
}

// evictCache removes entries nobody has used, and their objects.
//
// The object goes first: a row without its object is a cache miss, while an object with
// no row is invisible and would never be deleted (NFR-11, FL-07).
func evictCache(ctx context.Context, store Store, objects Objects, logger zerolog.Logger) error {
	idleBefore := time.Now().UTC().Add(-CacheMaxIdle)

	entries, err := store.EvictableCacheEntries(ctx, idleBefore, evictionBatch)
	if err != nil {
		return fmt.Errorf("list evictable entries: %w", err)
	}

	var evicted int
	for _, entry := range entries {
		if err := objects.Delete(ctx, entry.S3Key); err != nil {
			logger.Warn().Err(err).Str("s3_key", entry.S3Key).
				Msg("cached object not deleted; its row stays so the next sweep retries")
			continue
		}
		if err := store.DeleteCacheEntry(ctx, entry.CacheKey); err != nil {
			return fmt.Errorf("delete cache row: %w", err)
		}
		evicted++
	}

	if evicted > 0 {
		logger.Info().Int("evicted", evicted).Int("candidates", len(entries)).
			Msg("cache eviction complete")
	}
	return nil
}
