package cache_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pcaokhai/vitts/gateway/internal/cache"
)

// fakes keep the manager's decisions under test without Redis or S3: what is being
// checked here is the order of operations and the miss-vs-error boundary.
type fakeIndex struct {
	entries map[cache.Key]cache.Entry
	deletes int
	putErr  error
}

func newFakeIndex() *fakeIndex {
	return &fakeIndex{entries: make(map[cache.Key]cache.Entry)}
}

func (f *fakeIndex) Get(_ context.Context, key cache.Key) (cache.Entry, error) {
	entry, ok := f.entries[key]
	if !ok {
		return cache.Entry{}, cache.ErrMiss
	}
	return entry, nil
}

func (f *fakeIndex) Put(_ context.Context, key cache.Key, entry cache.Entry, _ time.Duration) error {
	if f.putErr != nil {
		return f.putErr
	}
	f.entries[key] = entry
	return nil
}

func (f *fakeIndex) Delete(_ context.Context, key cache.Key) error {
	f.deletes++
	delete(f.entries, key)
	return nil
}

type fakeStore struct {
	objects map[string][]byte
	putErr  error
}

func newFakeStore() *fakeStore { return &fakeStore{objects: make(map[string][]byte)} }

func (f *fakeStore) Get(_ context.Context, key string) (io.ReadCloser, error) {
	body, ok := f.objects[key]
	if !ok {
		return nil, cache.ErrMiss
	}
	return io.NopCloser(bytes.NewReader(body)), nil
}

func (f *fakeStore) Put(_ context.Context, key string, body io.Reader, _ int64) error {
	if f.putErr != nil {
		return f.putErr
	}
	raw, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	f.objects[key] = raw
	return nil
}

func (f *fakeStore) Delete(_ context.Context, key string) error {
	delete(f.objects, key)
	return nil
}

type fakeCatalogue struct {
	recorded int
	touched  int
}

func (f *fakeCatalogue) Record(context.Context, cache.Key, cache.Entry) error {
	f.recorded++
	return nil
}

func (f *fakeCatalogue) Touch(context.Context, cache.Key) error {
	f.touched++
	return nil
}

func newManager() (*cache.Manager, *fakeIndex, *fakeStore, *fakeCatalogue) {
	index, store, catalogue := newFakeIndex(), newFakeStore(), &fakeCatalogue{}
	return cache.NewManager(index, store, catalogue), index, store, catalogue
}

func TestLookupOfAnUnknownKeyIsAMiss(t *testing.T) {
	t.Parallel()

	manager, _, _, _ := newManager()

	_, _, err := manager.Lookup(context.Background(), cache.Derive(base()))

	require.ErrorIs(t, err, cache.ErrMiss)
}

func TestStoreThenLookupReturnsTheAudio(t *testing.T) {
	t.Parallel()

	manager, _, _, catalogue := newManager()
	ctx := context.Background()
	key := cache.Derive(base())
	audio := []byte("pcm frames")

	require.NoError(t, manager.Store(ctx, key, cache.Entry{
		Format: "pcm16_48k", DurationMS: 1200, Bytes: int64(len(audio)),
	}, bytes.NewReader(audio)))

	entry, body, err := manager.Lookup(ctx, key)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, body.Close()) })

	got, err := io.ReadAll(body)
	require.NoError(t, err)
	require.Equal(t, audio, got)
	require.Equal(t, cache.ObjectKey(key), entry.S3Key, "the object key follows the documented layout")
	require.Equal(t, 1, catalogue.recorded)
	require.Equal(t, 1, catalogue.touched, "a hit is counted")
}

func TestIndexEntryWithNoObjectDegradesToAMiss(t *testing.T) {
	t.Parallel()

	manager, index, store, _ := newManager()
	ctx := context.Background()
	key := cache.Derive(base())
	require.NoError(t, manager.Store(ctx, key, cache.Entry{Format: "pcm16_48k"}, bytes.NewReader(nil)))

	// The object was evicted from S3 but the index still points at it.
	require.NoError(t, store.Delete(ctx, cache.ObjectKey(key)))

	_, _, err := manager.Lookup(ctx, key)

	require.ErrorIs(t, err, cache.ErrMiss, "a vanished object must degrade to re-synthesis")
	require.Equal(t, 1, index.deletes, "the stale entry is dropped so the next caller pays once")
}

func TestStoreWritesTheObjectBeforeIndexingIt(t *testing.T) {
	t.Parallel()

	manager, index, store, _ := newManager()
	store.putErr = errors.New("s3 unavailable")

	err := manager.Store(context.Background(), cache.Derive(base()),
		cache.Entry{Format: "pcm16_48k"}, bytes.NewReader([]byte("x")))

	require.Error(t, err)
	require.Empty(t, index.entries, "an index entry pointing at nothing would break every later hit")
}

func TestObjectKeyFansOutByPrefix(t *testing.T) {
	t.Parallel()

	key := cache.Derive(base())
	objectKey := cache.ObjectKey(key)

	require.Equal(t, "cache/"+key.String()[:2]+"/"+key.String()+".pcm", objectKey)
}
