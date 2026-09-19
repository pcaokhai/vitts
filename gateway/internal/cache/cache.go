package cache

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"
)

// IndexTTL is the sliding expiry of the Redis index (docs/05-data-model.md § 2). The S3
// object outlives it; a lost index entry costs one re-synthesis, not a wrong answer.
const IndexTTL = 30 * 24 * time.Hour

// ErrMiss means the key is not cached. It is an ordinary outcome, not a failure.
var ErrMiss = errors.New("cache miss")

// Entry is what the index holds about one cached audio object.
type Entry struct {
	S3Key      string
	Format     string
	DurationMS int32
	Bytes      int64
}

// Index is the fast lookup in front of object storage.
type Index interface {
	Get(ctx context.Context, key Key) (Entry, error) // ErrMiss when absent
	Put(ctx context.Context, key Key, entry Entry, ttl time.Duration) error
	Delete(ctx context.Context, key Key) error
}

// Store is the object storage port.
type Store interface {
	Get(ctx context.Context, s3Key string) (io.ReadCloser, error)
	Put(ctx context.Context, s3Key string, body io.Reader, size int64) error
	Delete(ctx context.Context, s3Key string) error
}

// Catalogue is the durable record of cached objects, used by eviction and reporting.
type Catalogue interface {
	Record(ctx context.Context, key Key, entry Entry) error
	Touch(ctx context.Context, key Key) error
}

// Manager answers cache lookups and stores new audio.
type Manager struct {
	index     Index
	store     Store
	catalogue Catalogue
	ttl       time.Duration
}

// NewManager wires the three collaborators.
func NewManager(index Index, store Store, catalogue Catalogue) *Manager {
	return &Manager{index: index, store: store, catalogue: catalogue, ttl: IndexTTL}
}

// Lookup returns a reader for cached audio.
//
// The index is consulted first because it is the cheap question; a hit there still has to
// open the object, and an index entry whose object has gone is treated as a miss rather
// than an error, so a deleted object degrades to re-synthesis instead of a failed request.
func (m *Manager) Lookup(ctx context.Context, key Key) (Entry, io.ReadCloser, error) {
	entry, err := m.index.Get(ctx, key)
	if err != nil {
		return Entry{}, nil, err
	}

	body, err := m.store.Get(ctx, entry.S3Key)
	if err != nil {
		// The index outlived its object. Drop the stale entry so the next caller pays
		// one lookup instead of two.
		if delErr := m.index.Delete(ctx, key); delErr != nil {
			return Entry{}, nil, fmt.Errorf("drop stale index entry: %w", delErr)
		}
		return Entry{}, nil, ErrMiss
	}

	// Hit accounting is durable and off the read path's critical result: a failure to
	// record a hit must not fail the request the tenant is waiting on.
	if err := m.catalogue.Touch(ctx, key); err != nil {
		return entry, body, fmt.Errorf("touch cache entry: %w", err)
	}

	return entry, body, nil
}

// Store writes audio and indexes it.
//
// Object first, then index: an object with no index costs a re-synthesis, while an index
// pointing at nothing would send every caller down the stale path above.
func (m *Manager) Store(ctx context.Context, key Key, entry Entry, body io.Reader) error {
	if entry.S3Key == "" {
		entry.S3Key = ObjectKey(key)
	}

	if err := m.store.Put(ctx, entry.S3Key, body, entry.Bytes); err != nil {
		return fmt.Errorf("put audio: %w", err)
	}
	if err := m.catalogue.Record(ctx, key, entry); err != nil {
		return fmt.Errorf("record cache entry: %w", err)
	}
	if err := m.index.Put(ctx, key, entry, m.ttl); err != nil {
		return fmt.Errorf("index cache entry: %w", err)
	}
	return nil
}

// ObjectKey is the S3 layout from docs/05-data-model.md § 3: two hex characters of
// fan-out so one prefix does not accumulate every object.
func ObjectKey(key Key) string {
	hex := key.String()
	return fmt.Sprintf("cache/%s/%s.pcm", hex[:2], hex)
}
