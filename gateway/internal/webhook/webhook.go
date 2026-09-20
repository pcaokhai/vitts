// Package webhook delivers signed job-completion callbacks.
//
// It is an outbound adapter, not a use case: it speaks HTTP, so it lives outside
// internal/jobs, which may not (.claude/rules/gateway-go.md). It satisfies
// jobs.Notifier.
package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/rs/zerolog"

	"github.com/pcaokhai/vitts/gateway/internal/jobs"
	"github.com/pcaokhai/vitts/gateway/internal/telemetry"
)

// Webhook headers and delivery policy (US-15).
const (
	HeaderSignature = "X-ViTTS-Signature"
	HeaderTimestamp = "X-ViTTS-Timestamp"
	HeaderDelivery  = "X-ViTTS-Delivery"

	// MaxAttempts is the total number of delivery attempts (US-15 AC-2).
	MaxAttempts = 5
	// deliveryTimeout bounds one delivery. A receiver that cannot answer in this window
	// is treated as failed and retried.
	deliveryTimeout = 10 * time.Second
	// maxResponseBody caps what we read back, since we only need the status code.
	maxResponseBody = 4 << 10
)

// Backoff is the delay before each retry, exponential per US-15.
var Backoff = []time.Duration{
	5 * time.Second, 30 * time.Second, 2 * time.Minute, 10 * time.Minute,
}

// Payload is the body delivered to the tenant.
type Payload struct {
	Event       string            `json:"event"`
	JobID       string            `json:"job_id"`
	Status      string            `json:"status"`
	DurationMS  *int32            `json:"duration_ms,omitempty"`
	TotalChars  int32             `json:"total_chars"`
	Error       string            `json:"error,omitempty"`
	Metadata    map[string]string `json:"metadata"`
	CompletedAt *time.Time        `json:"completed_at,omitempty"`
}

// Dispatcher signs and delivers job-completion callbacks.
//
// Delivery happens on its own goroutine per job: a tenant's slow endpoint must not hold
// up the orchestrator, which has other jobs to finish.
type Dispatcher struct {
	client  *http.Client
	guard   jobs.WebhookGuard
	secret  []byte
	logger  zerolog.Logger
	metrics *telemetry.Metrics
	attempt func(ctx context.Context, d time.Duration) error
}

// SetMetrics attaches the instrument set. Optional, so tests run without one.
func (d *Dispatcher) SetMetrics(metrics *telemetry.Metrics) { d.metrics = metrics }

func (d *Dispatcher) countAttempt(outcome string) {
	if d.metrics != nil {
		d.metrics.WebhookAttempts.WithLabelValues(outcome).Inc()
	}
}

// NewDispatcher wires the dispatcher.
func NewDispatcher(secret string, guard jobs.WebhookGuard, logger zerolog.Logger) *Dispatcher {
	return &Dispatcher{
		client:  &http.Client{Timeout: deliveryTimeout},
		guard:   guard,
		secret:  []byte(secret),
		logger:  logger,
		attempt: sleepCtx,
	}
}

// JobFinished delivers a terminal job's webhook, if it asked for one.
func (d *Dispatcher) JobFinished(ctx context.Context, job jobs.Job) {
	if job.WebhookURL == "" {
		return
	}

	// Detached from the request that finished the job: delivery outlives it, and the
	// orchestrator must not wait on a tenant's endpoint.
	go d.deliver(context.WithoutCancel(ctx), job)
}

func (d *Dispatcher) deliver(ctx context.Context, job jobs.Job) {
	payload, err := json.Marshal(Payload{
		Event: "job." + string(job.Status), JobID: job.ID.String(), Status: string(job.Status),
		DurationMS: job.DurationMS, TotalChars: job.TotalChars, Error: job.Error,
		Metadata: jobs.PublicMetadata(job.Metadata), CompletedAt: job.CompletedAt,
	})
	if err != nil {
		d.logger.Error().Err(err).Str("job_id", job.ID.String()).Msg("webhook payload not built")
		return
	}

	for attempt := 1; attempt <= MaxAttempts; attempt++ {
		// Re-checked before every attempt: DNS can change between job creation and
		// delivery, and a name that was public then may point inward now (US-15 AC-3).
		if err := d.guard.Check(ctx, job.WebhookURL); err != nil {
			d.logger.Warn().Err(err).Str("job_id", job.ID.String()).
				Msg("webhook url rejected at delivery time")
			return
		}

		status, err := d.post(ctx, job, payload, attempt)
		if err == nil && status >= 200 && status < 300 {
			d.countAttempt("delivered")
			d.logger.Info().Str("job_id", job.ID.String()).
				Int("attempt", attempt).Int("status", status).Msg("webhook delivered")
			return
		}

		event := d.logger.Warn().Str("job_id", job.ID.String()).Int("attempt", attempt)
		if err != nil {
			event = event.Err(err)
		} else {
			event = event.Int("status", status)
		}
		event.Msg("webhook attempt failed")
		d.countAttempt("failed")

		if attempt == MaxAttempts {
			break
		}
		if sleepErr := d.attempt(ctx, Backoff[min(attempt-1, len(Backoff)-1)]); sleepErr != nil {
			return
		}
	}

	d.logger.Error().Str("job_id", job.ID.String()).
		Int("attempts", MaxAttempts).Msg("webhook gave up")
}

func (d *Dispatcher) post(ctx context.Context, job jobs.Job, payload []byte, attempt int) (int, error) {
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, job.WebhookURL, bytes.NewReader(payload))
	if err != nil {
		return 0, fmt.Errorf("build webhook request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(HeaderTimestamp, timestamp)
	request.Header.Set(HeaderSignature, Signature(d.secret, timestamp, payload))
	request.Header.Set(HeaderDelivery, job.ID.String()+"-"+strconv.Itoa(attempt))

	response, err := d.client.Do(request)
	if err != nil {
		return 0, fmt.Errorf("post webhook: %w", err)
	}
	defer func() { _ = response.Body.Close() }()

	// Drain a bounded amount so the connection can be reused; the body is not ours.
	_, _ = io.CopyN(io.Discard, response.Body, maxResponseBody)
	return response.StatusCode, nil
}

// Signature is `sha256=<hex>` over "<timestamp>.<body>".
//
// The timestamp is inside the signed material, so a captured delivery cannot be replayed
// later with a fresh timestamp header (US-15 acceptance criterion 1).
func Signature(secret []byte, timestamp string, payload []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(timestamp))
	mac.Write([]byte("."))
	mac.Write(payload)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// VerifySignature is what a receiver implements. It lives here so the contract has one
// definition and the SDKs can copy it verbatim.
func VerifySignature(secret []byte, timestamp string, payload []byte, presented string) bool {
	expected := Signature(secret, timestamp, payload)
	return hmac.Equal([]byte(expected), []byte(presented))
}

// sleepCtx waits for d unless the caller goes away first.
func sleepCtx(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// SetBackoffForTest removes retry delays, so a test exercises the retry policy rather
// than the clock.
func (d *Dispatcher) SetBackoffForTest(sleep func(context.Context, time.Duration) error) {
	d.attempt = sleep
}
