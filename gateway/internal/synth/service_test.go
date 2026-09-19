package synth_test

import (
	"bytes"
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/pcaokhai/vitts/gateway/internal/audio"
	"github.com/pcaokhai/vitts/gateway/internal/cache"
	"github.com/pcaokhai/vitts/gateway/internal/dispatch"
	"github.com/pcaokhai/vitts/gateway/internal/dispatch/fakeworker"
	workerpb "github.com/pcaokhai/vitts/gateway/internal/gen/workerpb"
	"github.com/pcaokhai/vitts/gateway/internal/synth"
	"github.com/pcaokhai/vitts/gateway/internal/voices"
)

type fakeQuota struct {
	allowed  bool
	used     int64
	recorded int64
	checkErr error
}

func (f *fakeQuota) Check(context.Context, uuid.UUID, int64, int64) (synth.Allowance, error) {
	if f.checkErr != nil {
		return synth.Allowance{}, f.checkErr
	}
	return synth.Allowance{Allowed: f.allowed, Used: f.used}, nil
}

func (f *fakeQuota) Record(_ context.Context, _ uuid.UUID, chars int64) error {
	f.recorded += chars
	return nil
}

type memCache struct {
	mu      sync.Mutex
	entries map[cache.Key][]byte
	meta    map[cache.Key]cache.Entry
	stores  int
}

func newMemCache() *memCache {
	return &memCache{entries: map[cache.Key][]byte{}, meta: map[cache.Key]cache.Entry{}}
}

func (m *memCache) Lookup(_ context.Context, key cache.Key) (cache.Entry, io.ReadCloser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	body, ok := m.entries[key]
	if !ok {
		return cache.Entry{}, nil, cache.ErrMiss
	}
	return m.meta[key], io.NopCloser(bytes.NewReader(body)), nil
}

func (m *memCache) Store(_ context.Context, key cache.Key, entry cache.Entry, body io.Reader) error {
	raw, err := io.ReadAll(body)
	if err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries[key], m.meta[key], m.stores = raw, entry, m.stores+1
	return nil
}

type fakeCatalogue struct {
	voice voices.Voice
	err   error
}

func (f fakeCatalogue) Resolve(context.Context, *uuid.UUID, string) (voices.Voice, error) {
	if f.err != nil {
		return voices.Voice{}, f.err
	}
	return f.voice, nil
}

type fakePlans struct{ allowance int64 }

func (f fakePlans) CharsPerMonth(context.Context, string) (int64, error) { return f.allowance, nil }

type recordingMeter struct {
	mu    sync.Mutex
	usage []synth.Usage
}

func (m *recordingMeter) Record(_ context.Context, usage synth.Usage) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.usage = append(m.usage, usage)
}

func (m *recordingMeter) last() synth.Usage {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.usage[len(m.usage)-1]
}

type harness struct {
	service *synth.Service
	quota   *fakeQuota
	cache   *memCache
	meter   *recordingMeter
	worker  *fakeworker.Server
}

func newHarness(t *testing.T) harness {
	t.Helper()

	worker, err := fakeworker.Start()
	require.NoError(t, err)
	t.Cleanup(worker.Stop)
	worker.SetHealth(&workerpb.HealthResponse{
		Ready: true, ModelVersion: "model-1", SlotsTotal: 2,
		VoiceIds: []string{"maichi"},
	})

	pool, err := dispatch.NewPool([]string{worker.Addr()}, zerolog.New(io.Discard),
		dispatch.Options{HealthInterval: 10 * time.Millisecond, ProbeInterval: 20 * time.Millisecond, Tick: 5 * time.Millisecond})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, pool.Close()) })
	pool.Start(context.Background())

	require.Eventually(t, func() bool { return pool.Ready(context.Background()) == nil },
		5*time.Second, 20*time.Millisecond, "pool never became ready")

	q := &fakeQuota{allowed: true}
	c := newMemCache()
	m := &recordingMeter{}

	return harness{
		service: synth.NewService(q, c, dispatch.NewDispatcher(pool),
			fakeCatalogue{voice: voices.Voice{ID: "maichi", ModelVersion: "model-1", Enabled: true}},
			fakePlans{allowance: 1_000_000}, m, zerolog.New(io.Discard)),
		quota: q, cache: c, meter: m, worker: worker,
	}
}

func request() synth.Request {
	return synth.Request{
		TenantID: uuid.New(), KeyID: uuid.New(), RequestID: "r-1",
		Text: "Xin chào các bạn.", VoiceID: "maichi", Normalize: true,
	}
}

func TestSynthesizeReturnsPlayableWAV(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.worker.SetAudio(3, 480)

	result, err := h.service.Synthesize(context.Background(), request(), "dev")

	require.NoError(t, err)
	require.False(t, result.CacheHit)
	require.Equal(t, "RIFF", string(result.Audio[:4]), "the response is a WAV, not raw PCM")
	require.Equal(t, "WAVE", string(result.Audio[8:12]))
	require.Len(t, result.Audio, audio.HeaderSize+3*480)
	require.Equal(t, audio.DurationMS(3*480, 48000), result.DurationMS)
	require.Equal(t, int64(len([]rune(request().Text))), result.CharsBilled)
}

// T-08: a second identical request must be served from cache and touch no worker.
func TestSecondIdenticalRequestIsACacheHit(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	ctx := context.Background()

	first, err := h.service.Synthesize(ctx, request(), "dev")
	require.NoError(t, err)
	require.Equal(t, 1, h.worker.Syntheses())

	second, err := h.service.Synthesize(ctx, request(), "dev")

	require.NoError(t, err)
	require.True(t, second.CacheHit)
	require.Equal(t, 1, h.worker.Syntheses(), "a hit must not reach a worker")
	require.Equal(t, first.Audio, second.Audio, "the hit returns the same audio")
}

// US-11 acceptance criterion 4: a cache hit still bills.
func TestCacheHitStillBillsQuotaAndUsage(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	ctx := context.Background()
	req := request()

	_, err := h.service.Synthesize(ctx, req, "dev")
	require.NoError(t, err)
	_, err = h.service.Synthesize(ctx, req, "dev")
	require.NoError(t, err)

	require.Equal(t, 2*req.Chars(), h.quota.recorded, "both requests consume allowance")
	require.True(t, h.meter.last().Cached)
	require.Equal(t, "ok", h.meter.last().Status)
}

func TestQuotaExceededIsRefusedBeforeAnyWork(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.quota.allowed = false

	_, err := h.service.Synthesize(context.Background(), request(), "dev")

	require.ErrorIs(t, err, synth.ErrQuotaExceeded)
	require.Zero(t, h.worker.Syntheses(), "a refused request must not cost the fleet")
	require.Zero(t, h.quota.recorded)
}

func TestUnknownVoiceIsRefusedBeforeAnyWork(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	service := synth.NewService(h.quota, h.cache, dispatch.NewDispatcher(mustPool(t, h.worker)),
		fakeCatalogue{err: voices.ErrUnknown}, fakePlans{allowance: 1000}, h.meter,
		zerolog.New(io.Discard))

	_, err := service.Synthesize(context.Background(), request(), "dev")

	require.ErrorIs(t, err, synth.ErrUnknownVoice)
	require.Zero(t, h.worker.Syntheses())
}

func TestWorkerFailureIsReportedAndNotBilled(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.worker.SetFailing(true)

	_, err := h.service.Synthesize(context.Background(), request(), "dev")

	require.ErrorIs(t, err, synth.ErrWorkerFailed)
	require.Zero(t, h.quota.recorded, "work we did not deliver must not be billed")
}

func TestDifferentTextMissesTheCache(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	ctx := context.Background()

	_, err := h.service.Synthesize(ctx, request(), "dev")
	require.NoError(t, err)

	other := request()
	other.Text = "Một câu khác."
	result, err := h.service.Synthesize(ctx, other, "dev")

	require.NoError(t, err)
	require.False(t, result.CacheHit)
	require.Equal(t, 2, h.worker.Syntheses())
}

func mustPool(t *testing.T, worker *fakeworker.Server) *dispatch.Pool {
	t.Helper()

	pool, err := dispatch.NewPool([]string{worker.Addr()}, zerolog.New(io.Discard),
		dispatch.Options{HealthInterval: 10 * time.Millisecond, ProbeInterval: 20 * time.Millisecond, Tick: 5 * time.Millisecond})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, pool.Close()) })
	pool.Start(context.Background())
	return pool
}
