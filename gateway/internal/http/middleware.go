package http

import (
	"context"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

// HeaderRequestID is set on every response and echoed from the request when present.
const HeaderRequestID = "X-Request-Id"

// maxRequestIDLen bounds an inbound id: it reaches logs, so it is untrusted input.
const maxRequestIDLen = 64

type requestIDKey struct{}

// RequestIDFrom returns the id assigned to this request, or "" outside a request.
func RequestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

// RequestID assigns an id to every request, preferring a caller-supplied one so a trace
// spans the caller's system and ours. Sanitised, because it is echoed and logged.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := sanitiseRequestID(r.Header.Get(HeaderRequestID))
		if id == "" {
			id = uuid.NewString()
		}

		w.Header().Set(HeaderRequestID, id)
		ctx := context.WithValue(r.Context(), requestIDKey{}, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// sanitiseRequestID keeps ASCII letters, digits and -_. and drops everything else, so a
// caller cannot inject newlines or control characters into a log line.
func sanitiseRequestID(raw string) string {
	if len(raw) > maxRequestIDLen {
		raw = raw[:maxRequestIDLen]
	}

	var b strings.Builder
	for _, r := range raw {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-', r == '_', r == '.':
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Logger puts a request-scoped logger on the context and writes one access line per
// request. Only metadata is logged: never a body, a header, or user text (ADR-008).
func Logger(base zerolog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			started := time.Now()
			logger := base.With().
				Str("request_id", RequestIDFrom(r.Context())).
				Str("method", r.Method).
				Str("path", r.URL.Path).
				Logger()

			recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(recorder, r.WithContext(logger.WithContext(r.Context())))

			logger.Info().
				Int("status", recorder.status).
				Dur("duration", time.Since(started)).
				Msg("request")
		})
	}
}

// Recover turns a panic into a problem+json 500 instead of a dropped connection.
//
// docs/14-engineering-standards.md forbids panic in request paths; this is the net under
// that rule, not permission to rely on it.
func Recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		//nolint:contextcheck // the request context travels on r, which the closure captures
		defer func() {
			recovered := recover()
			if recovered == nil {
				return
			}

			zerolog.Ctx(r.Context()).Error().
				Interface("panic", recovered).
				Bytes("stack", debug.Stack()).
				Msg("panic recovered")

			WriteProblem(w, r, NewError(CodeInternal, ""))
		}()

		next.ServeHTTP(w, r)
	})
}

// statusRecorder remembers the status code so the access log can report it.
type statusRecorder struct {
	http.ResponseWriter
	status  int
	written bool
}

func (s *statusRecorder) WriteHeader(status int) {
	if s.written {
		return
	}
	s.status = status
	s.written = true
	s.ResponseWriter.WriteHeader(status)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if !s.written {
		s.written = true
	}
	n, err := s.ResponseWriter.Write(b)
	if err != nil {
		return n, err //nolint:wrapcheck // passthrough to the real ResponseWriter
	}
	return n, nil
}

// Unwrap lets http.ResponseController reach the underlying writer, which the streaming
// endpoint in task 1.10 needs for flushing.
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }
