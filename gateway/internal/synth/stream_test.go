package synth_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/pcaokhai/vitts/gateway/internal/synth"
)

// collectingSink records what a client would have received.
type collectingSink struct {
	mu         sync.Mutex
	headers    int
	sampleRate int32
	chunks     [][]byte
	// failAfter makes Chunk fail once this many chunks have been written, which is how a
	// disconnected client is simulated.
	failAfter int
	onChunk   func(int)
}

func (s *collectingSink) Header(sampleRate int32) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.headers++
	s.sampleRate = sampleRate
	return nil
}

func (s *collectingSink) Chunk(pcm []byte) error {
	s.mu.Lock()
	count := len(s.chunks)
	s.mu.Unlock()

	if s.onChunk != nil {
		s.onChunk(count)
	}
	if s.failAfter > 0 && count >= s.failAfter {
		return errors.New("client gone")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	frame := make([]byte, len(pcm))
	copy(frame, pcm)
	s.chunks = append(s.chunks, frame)
	return nil
}

func (s *collectingSink) bytes() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	var n int
	for _, chunk := range s.chunks {
		n += len(chunk)
	}
	return n
}

// fakeLeases hands out slots up to a limit and records renewals and releases.
type fakeLeases struct {
	mu       sync.Mutex
	held     int32
	limit    int32
	renews   int
	releases int
}

func (f *fakeLeases) Acquire(_ context.Context, _ uuid.UUID, limit int32) (synth.Lease, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.limit == 0 {
		f.limit = limit
	}
	if f.held >= f.limit {
		return nil, false, nil
	}
	f.held++
	return &fakeLease{owner: f}, true, nil
}

type fakeLease struct{ owner *fakeLeases }

func (l *fakeLease) Renew(context.Context) error {
	l.owner.mu.Lock()
	defer l.owner.mu.Unlock()
	l.owner.renews++
	return nil
}

func (l *fakeLease) Release(context.Context) {
	l.owner.mu.Lock()
	defer l.owner.mu.Unlock()
	l.owner.held--
	l.owner.releases++
}

func (f *fakeLeases) releaseCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.releases
}

func TestStreamDeliversFramesAndCachesThem(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.worker.SetAudio(4, 240)
	sink := &collectingSink{}
	leases := &fakeLeases{limit: 2}

	result, err := h.service.Stream(context.Background(), request(), "dev", sink, leases, 2)

	require.NoError(t, err)
	require.Equal(t, 1, sink.headers, "the header is written exactly once, before any audio")
	require.Len(t, sink.chunks, 4, "frames reach the client as they arrive")
	require.Equal(t, 4*240, sink.bytes())
	require.False(t, result.Cancelled)
	require.Equal(t, 1, h.cache.stores, "a completed stream is teed to the cache")
	require.Equal(t, 1, leases.releaseCount(), "the slot is returned")
}

func TestStreamServesASecondRequestFromCache(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.worker.SetAudio(3, 240)
	leases := &fakeLeases{limit: 2}
	ctx := context.Background()

	_, err := h.service.Stream(ctx, request(), "dev", &collectingSink{}, leases, 2)
	require.NoError(t, err)

	sink := &collectingSink{}
	result, err := h.service.Stream(ctx, request(), "dev", sink, leases, 2)

	require.NoError(t, err)
	require.True(t, result.CacheHit)
	require.Equal(t, 1, h.worker.Syntheses(), "a cached stream must not reach a worker")
	require.Equal(t, 3*240, sink.bytes(), "the client still receives the whole audio")
}

// T-07: a disconnected client stops inference and is billed only for what it received.
func TestClientDisconnectStopsTheStreamAndBillsWhatWasDelivered(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.worker.SetAudio(100, 4800) // 0.05 s of audio per frame
	sink := &collectingSink{failAfter: 3}
	leases := &fakeLeases{limit: 2}

	result, err := h.service.Stream(context.Background(), request(), "dev", sink, leases, 2)

	require.NoError(t, err, "a client hanging up is not a server error")
	require.True(t, result.Cancelled)
	require.Len(t, sink.chunks, 3)
	require.Zero(t, h.cache.stores, "partial audio must never be cached")
	require.Equal(t, 1, leases.releaseCount(), "a cancelled stream frees its slot immediately")
	full := request()
	require.Less(t, result.CharsBilled, full.Chars(),
		"a cancelled stream bills the audio delivered, not the text requested (ADR-010)")
	require.Equal(t, result.CharsBilled, h.quota.recorded)
}

func TestContextCancellationStopsTheStream(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.worker.SetAudio(200, 4800)

	ctx, cancel := context.WithCancel(context.Background())
	sink := &collectingSink{onChunk: func(count int) {
		if count == 2 {
			cancel()
		}
	}}

	started := time.Now()
	result, err := h.service.Stream(ctx, request(), "dev", sink, &fakeLeases{limit: 2}, 2)
	elapsed := time.Since(started)

	require.NoError(t, err)
	require.True(t, result.Cancelled)
	require.Less(t, elapsed, 5*time.Second, "cancellation must not wait for the full synthesis")
	require.Zero(t, h.cache.stores)
}

func TestStreamRefusesWhenTheTenantIsAtItsConcurrencyLimit(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	leases := &fakeLeases{limit: 1}
	_, _, err := leases.Acquire(context.Background(), uuid.New(), 1)
	require.NoError(t, err)

	_, err = h.service.Stream(context.Background(), request(), "dev", &collectingSink{}, leases, 1)

	require.ErrorIs(t, err, synth.ErrConcurrencyLimited)
	require.Zero(t, h.worker.Syntheses(), "a refused stream must not cost the fleet")
}

func TestStreamRefusesBeforeTheHeaderWhenQuotaIsExhausted(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.quota.allowed = false
	sink := &collectingSink{}

	_, err := h.service.Stream(context.Background(), request(), "dev", sink, &fakeLeases{limit: 2}, 2)

	require.ErrorIs(t, err, synth.ErrQuotaExceeded)
	require.Zero(t, sink.headers,
		"every refusal must happen before the first byte, or the status is already committed")
}
