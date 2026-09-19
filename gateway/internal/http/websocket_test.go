package http_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/pcaokhai/vitts/gateway/internal/auth"
	gatewayhttp "github.com/pcaokhai/vitts/gateway/internal/http"
	"github.com/pcaokhai/vitts/gateway/internal/ratelimit"
	"github.com/pcaokhai/vitts/gateway/internal/synth"
)

// scriptedStreamer plays a fixed number of chunks, pausing between them so a test can
// cancel mid-utterance.
type scriptedStreamer struct {
	chunks    int
	chunkSize int
	delay     time.Duration

	mu        sync.Mutex
	calls     int
	cancelled int
	delivered int
}

func (s *scriptedStreamer) Stream(
	ctx context.Context, req synth.Request, _ string,
	sink synth.StreamSink, leases synth.Leases, limit int32,
) (synth.StreamResult, error) {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()

	if err := req.Validate(); err != nil {
		return synth.StreamResult{}, err
	}

	lease, ok, err := leases.Acquire(ctx, req.TenantID, limit)
	if err != nil {
		return synth.StreamResult{}, err
	}
	if !ok {
		return synth.StreamResult{}, synth.ErrConcurrencyLimited
	}
	defer lease.Release(context.WithoutCancel(ctx))

	if err := sink.Header(48000); err != nil {
		return synth.StreamResult{}, err
	}

	for i := range s.chunks {
		select {
		case <-ctx.Done():
			s.mu.Lock()
			s.cancelled++
			s.mu.Unlock()
			return synth.StreamResult{Cancelled: true, CharsBilled: int64(i)}, nil
		case <-time.After(s.delay):
		}

		if err := sink.Chunk(make([]byte, s.chunkSize)); err != nil {
			s.mu.Lock()
			s.cancelled++
			s.mu.Unlock()
			return synth.StreamResult{Cancelled: true}, nil
		}
		s.mu.Lock()
		s.delivered++
		s.mu.Unlock()
	}

	return synth.StreamResult{DurationMS: 1200, CharsBilled: req.Chars()}, nil
}

func (s *scriptedStreamer) stats() (calls, cancelled, delivered int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls, s.cancelled, s.delivered
}

// memLeases is an in-process concurrency limiter for the socket tests.
type memLeases struct {
	mu   sync.Mutex
	held int32
}

func (m *memLeases) Acquire(_ context.Context, _ uuid.UUID, limit int32) (synth.Lease, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.held >= limit {
		return nil, false, nil
	}
	m.held++
	return memLease{owner: m}, true, nil
}

type memLease struct{ owner *memLeases }

func (l memLease) Renew(context.Context) error { return nil }

func (l memLease) Release(context.Context) {
	l.owner.mu.Lock()
	defer l.owner.mu.Unlock()
	l.owner.held--
}

type stubStreamLimits struct{ limit int32 }

func (s stubStreamLimits) MaxConcurrentStreams(context.Context, string) (int32, error) {
	return s.limit, nil
}

// dialWS starts a server exposing the websocket route behind the real auth chain.
func dialWS(t *testing.T, streamer *scriptedStreamer) (*websocket.Conn, *memLeases) {
	t.Helper()

	leases := &memLeases{}
	repo := repoFunc(func(context.Context, []byte) (auth.Identity, error) {
		return liveIdentity(auth.ScopeSynth), nil
	})
	handler := gatewayhttp.Router(gatewayhttp.Deps{
		Logger:    zerolog.New(&strings.Builder{}),
		Readiness: gatewayhttp.NewReadiness(),
		Auth:      auth.NewAuthenticator(repo),
		Limiter:   &stubLimiter{decision: ratelimit.RateDecision{Allowed: true, Limit: 60, Remaining: 59}},
		Plans:     stubPlans{perMinute: 60},
		TenantAPI: []gatewayhttp.Route{{
			Method: http.MethodGet, Pattern: "/synthesize/ws", Scope: auth.ScopeSynth,
			Handler: gatewayhttp.SynthesizeWS(streamer, leases, stubStreamLimits{limit: 2}),
		}},
	})

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	conn, resp, err := websocket.Dial(context.Background(),
		"ws"+strings.TrimPrefix(server.URL, "http")+"/v1/synthesize/ws",
		&websocket.DialOptions{HTTPHeader: http.Header{
			"Authorization": []string{"Bearer " + goodSecret},
		}})
	if resp != nil && resp.Body != nil {
		require.NoError(t, resp.Body.Close())
	}
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.CloseNow() })

	return conn, leases
}

func sendJSON(t *testing.T, conn *websocket.Conn, payload string) {
	t.Helper()
	require.NoError(t, conn.Write(context.Background(), websocket.MessageText, []byte(payload)))
}

// readUntilEvent collects binary frames until a text event arrives.
func readUntilEvent(t *testing.T, conn *websocket.Conn) (binaryFrames int, event map[string]any) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	for {
		kind, data, err := conn.Read(ctx)
		require.NoError(t, err)

		if kind == websocket.MessageBinary {
			binaryFrames++
			continue
		}

		require.NoError(t, json.Unmarshal(data, &event))
		return binaryFrames, event
	}
}

// US-10 acceptance criterion 1.
func TestWebSocketStreamsAnUtteranceAndEndsIt(t *testing.T) {
	t.Parallel()

	streamer := &scriptedStreamer{chunks: 3, chunkSize: 480}
	conn, _ := dialWS(t, streamer)

	sendJSON(t, conn, `{"text":"Xin chào các bạn.","voice":"maichi"}`)
	frames, event := readUntilEvent(t, conn)

	require.Equal(t, 4, frames, "one header frame plus three audio frames")
	require.Equal(t, "end", event["type"])
	require.EqualValues(t, 1200, event["duration_ms"])
}

// US-10 acceptance criterion 2: cancel stops the utterance and leaves the socket usable.
func TestWebSocketCancelStopsTheUtteranceAndKeepsTheSocket(t *testing.T) {
	t.Parallel()

	streamer := &scriptedStreamer{chunks: 50, chunkSize: 480, delay: 20 * time.Millisecond}
	conn, leases := dialWS(t, streamer)

	sendJSON(t, conn, `{"text":"Một đoạn dài để có thời gian ngắt.","voice":"maichi"}`)

	// Wait for audio to be flowing, then barge in.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	kind, _, err := conn.Read(ctx)
	require.NoError(t, err)
	require.Equal(t, websocket.MessageBinary, kind)

	started := time.Now()
	sendJSON(t, conn, `{"type":"cancel"}`)
	_, event := readUntilEvent(t, conn)
	elapsed := time.Since(started)

	require.Equal(t, "end", event["type"])
	require.Equal(t, true, event["cancelled"])
	require.Less(t, elapsed, 2*time.Second, "cancel must stop the utterance promptly")

	_, cancelled, delivered := streamer.stats()
	require.Equal(t, 1, cancelled)
	require.Less(t, delivered, 50, "inference stopped early")

	// The socket is still usable: that is the whole point of an explicit cancel.
	streamer.chunks, streamer.delay = 2, 0
	sendJSON(t, conn, `{"text":"Câu tiếp theo.","voice":"maichi"}`)
	frames, second := readUntilEvent(t, conn)

	require.Equal(t, "end", second["type"])
	require.Positive(t, frames)

	leases.mu.Lock()
	defer leases.mu.Unlock()
	require.Zero(t, leases.held, "every utterance released its slot")
}

func TestWebSocketReportsAnInvalidRequestWithoutClosing(t *testing.T) {
	t.Parallel()

	streamer := &scriptedStreamer{chunks: 1, chunkSize: 16}
	conn, _ := dialWS(t, streamer)

	sendJSON(t, conn, `{"text":"","voice":"maichi"}`)
	_, event := readUntilEvent(t, conn)

	require.Equal(t, "error", event["type"])
	require.Equal(t, string(gatewayhttp.CodeInvalidRequest), event["code"])

	// Still usable afterwards.
	sendJSON(t, conn, `{"text":"Bây giờ thì hợp lệ.","voice":"maichi"}`)
	_, ok := readUntilEvent(t, conn)
	require.Equal(t, "end", ok["type"])
}

func TestWebSocketRequiresAKey(t *testing.T) {
	t.Parallel()

	streamer := &scriptedStreamer{}
	leases := &memLeases{}
	repo := repoFunc(func(context.Context, []byte) (auth.Identity, error) {
		return auth.Identity{}, auth.ErrUnauthorized
	})
	handler := gatewayhttp.Router(gatewayhttp.Deps{
		Logger:    zerolog.New(&strings.Builder{}),
		Readiness: gatewayhttp.NewReadiness(),
		Auth:      auth.NewAuthenticator(repo),
		Limiter:   &stubLimiter{decision: ratelimit.RateDecision{Allowed: true}},
		Plans:     stubPlans{perMinute: 60},
		TenantAPI: []gatewayhttp.Route{{
			Method: http.MethodGet, Pattern: "/synthesize/ws", Scope: auth.ScopeSynth,
			Handler: gatewayhttp.SynthesizeWS(streamer, leases, stubStreamLimits{limit: 1}),
		}},
	})
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	_, resp, err := websocket.Dial(context.Background(),
		"ws"+strings.TrimPrefix(server.URL, "http")+"/v1/synthesize/ws", nil)
	if resp != nil && resp.Body != nil {
		require.NoError(t, resp.Body.Close())
	}

	require.Error(t, err, "an unauthenticated upgrade must be refused")
}
