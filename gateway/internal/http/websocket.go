package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/coder/websocket"
	"github.com/rs/zerolog"

	"github.com/pcaokhai/vitts/gateway/internal/audio"
	"github.com/pcaokhai/vitts/gateway/internal/auth"
	"github.com/pcaokhai/vitts/gateway/internal/synth"
)

// WebSocket protocol constants from docs/api/openapi.yaml.
const (
	wsMessageCancel = "cancel"
	wsMessageEnd    = "end"
	// wsReadLimit bounds a single text frame. Requests are small; anything larger is a
	// client bug or an attempt to make us allocate.
	wsReadLimit = 1 << 20
	// wsIdleTimeout closes a socket whose client has gone quiet, so an abandoned
	// connection cannot hold a concurrency lease forever.
	wsIdleTimeout = 5 * time.Minute
)

type wsControl struct {
	Type string `json:"type"`
}

type wsEvent struct {
	Type       string `json:"type"`
	DurationMS int32  `json:"duration_ms,omitempty"`
	Cached     bool   `json:"cached,omitempty"`
	Cancelled  bool   `json:"cancelled,omitempty"`
	Code       string `json:"code,omitempty"`
	Detail     string `json:"detail,omitempty"`
}

// SynthesizeWS handles GET /v1/synthesize/ws (US-10).
//
// One socket carries many utterances: a cancel stops the current one and leaves the
// socket open for the next, which is what barge-in needs. Tearing down the connection
// per utterance would cost a TLS handshake every time a user interrupts.
func SynthesizeWS(service Streamer, leases synth.Leases, limits StreamLimits) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		identity, ok := IdentityFrom(r.Context())
		if !ok {
			WriteProblem(w, r, NewError(CodeUnauthorized, "missing or malformed API key"))
			return
		}

		streamLimit, err := limits.MaxConcurrentStreams(r.Context(), identity.PlanID)
		if err != nil {
			WriteProblem(w, r, &Error{Code: CodeInternal, Cause: err})
			return
		}

		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			// Same-origin by default; a browser client must be served from our domain
			// or use a key from a backend, which is what the contract assumes.
			CompressionMode: websocket.CompressionDisabled,
		})
		if err != nil {
			// Accept has already written its own response.
			zerolog.Ctx(r.Context()).Debug().Err(err).Msg("websocket upgrade refused")
			return
		}
		defer func() { _ = conn.CloseNow() }()

		conn.SetReadLimit(wsReadLimit)
		session := &wsSession{
			conn: conn, service: service, leases: leases,
			identity: identity, streamLimit: streamLimit,
			requestID: RequestIDFrom(r.Context()),
			logger:    *zerolog.Ctx(r.Context()),
			requests:  make(chan []byte),
			cancels:   make(chan struct{}, 1),
		}
		session.run(r.Context())
	}
}

type wsSession struct {
	conn        *websocket.Conn
	service     Streamer
	leases      synth.Leases
	identity    auth.Identity
	streamLimit int32
	requestID   string
	logger      zerolog.Logger

	// One goroutine reads the socket for its whole life. Cancelling a read mid-frame
	// closes the connection in this library, so an utterance must never own the reader:
	// it subscribes to what the reader classifies instead.
	requests chan []byte
	cancels  chan struct{}
}

// run consumes utterance requests until the socket goes away.
func (s *wsSession) run(ctx context.Context) {
	go s.readLoop(ctx)

	for data := range s.requests {
		// A cancel that arrived after the previous utterance finished is stale; it must
		// not abort the next one.
		s.drainCancels()
		s.utterance(ctx, data)
	}
}

// readLoop classifies every inbound frame: control messages become signals, everything
// else is an utterance request.
func (s *wsSession) readLoop(ctx context.Context) {
	defer close(s.requests)

	for {
		readCtx, cancelRead := context.WithTimeout(ctx, wsIdleTimeout)
		kind, data, err := s.conn.Read(readCtx)
		cancelRead()
		if err != nil {
			return // client closed, went idle, or the socket broke
		}
		if kind != websocket.MessageText {
			s.fail(ctx, CodeInvalidRequest, "the request frame must be text")
			continue
		}

		var control wsControl
		if json.Unmarshal(data, &control) == nil && control.Type != "" {
			if control.Type == wsMessageCancel {
				select {
				case s.cancels <- struct{}{}:
				default: // a cancel is already pending; one is enough
				}
			}
			continue
		}

		select {
		case s.requests <- data:
		case <-ctx.Done():
			return
		}
	}
}

func (s *wsSession) drainCancels() {
	for {
		select {
		case <-s.cancels:
		default:
			return
		}
	}
}

// utterance synthesizes one request, abortable by a cancel message (US-10 AC-2).
func (s *wsSession) utterance(ctx context.Context, request []byte) {
	var body synthesizeRequest
	if err := json.Unmarshal(request, &body); err != nil {
		s.fail(ctx, CodeInvalidRequest, "body must match SynthesizeRequest")
		return
	}

	utteranceCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	watching := make(chan struct{})
	go func() {
		defer close(watching)
		select {
		case <-s.cancels:
			cancel()
		case <-utteranceCtx.Done():
		}
	}()

	req := body.toUseCase(s.identity.TenantID, s.identity.KeyID, s.requestID)
	// Writes use the session context, not the utterance one: cancelling a write's
	// context would close the socket, and the point of an explicit cancel is that the
	// socket survives it.
	sink := &wsSink{conn: s.conn, ctx: ctx}

	result, err := s.service.Stream(utteranceCtx, req, s.identity.PlanID, sink, s.leases, s.streamLimit)
	cancel()
	<-watching

	if err != nil {
		var mapped *Error
		if errors.As(mapStreamError(err), &mapped) {
			s.fail(ctx, mapped.Code, mapped.Detail)
			return
		}
		s.fail(ctx, CodeInternal, "")
		return
	}

	s.send(ctx, wsEvent{
		Type: wsMessageEnd, DurationMS: result.DurationMS,
		Cached: result.CacheHit, Cancelled: result.Cancelled,
	})
}

func (s *wsSession) send(ctx context.Context, event wsEvent) {
	payload, err := json.Marshal(event)
	if err != nil {
		return
	}
	if err := s.conn.Write(ctx, websocket.MessageText, payload); err != nil {
		s.logger.Debug().Err(err).Msg("websocket event not delivered")
	}
}

func (s *wsSession) fail(ctx context.Context, code Code, detail string) {
	// Logged as well as sent: an error a client sees but operations cannot is a blind
	// spot, which is exactly how this path's first bug stayed hidden.
	s.logger.Warn().Str("code", string(code)).Str("detail", detail).Msg("websocket utterance failed")
	s.send(ctx, wsEvent{Type: "error", Code: string(code), Detail: detail})
}

// wavHeaderBuffer collects the 44-byte header so it can be sent as one frame.
type wavHeaderBuffer struct {
	buf []byte
}

func (b *wavHeaderBuffer) Write(p []byte) (int, error) {
	b.buf = append(b.buf, p...)
	return len(p), nil
}

func (b *wavHeaderBuffer) Bytes() []byte { return b.buf }

// wsSink writes audio as binary frames.
type wsSink struct {
	conn *websocket.Conn
	ctx  context.Context
}

func (s *wsSink) Header(sampleRate int32) error {
	var header wavHeaderBuffer
	if err := audio.WriteHeader(&header, sampleRate, audio.UnknownSize); err != nil {
		return err
	}
	if err := s.conn.Write(s.ctx, websocket.MessageBinary, header.Bytes()); err != nil {
		return err //nolint:wrapcheck // the caller treats a write failure as a disconnect
	}
	return nil
}

func (s *wsSink) Chunk(pcm []byte) error {
	if err := s.conn.Write(s.ctx, websocket.MessageBinary, pcm); err != nil {
		return err //nolint:wrapcheck // the caller treats a write failure as a disconnect
	}
	return nil
}
