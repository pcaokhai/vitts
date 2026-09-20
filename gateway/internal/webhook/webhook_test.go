package webhook_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/pcaokhai/vitts/gateway/internal/jobs"
	"github.com/pcaokhai/vitts/gateway/internal/webhook"
)

const webhookSecret = "a-shared-secret-for-tests"

// receiver records deliveries and answers with a scripted sequence of status codes.
type receiver struct {
	mu         sync.Mutex
	statuses   []int
	deliveries []delivery
	server     *httptest.Server
}

type delivery struct {
	body       []byte
	signature  string
	timestamp  string
	deliveryID string
}

func newReceiver(t *testing.T, statuses ...int) *receiver {
	t.Helper()

	r := &receiver{statuses: statuses}
	r.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)

		r.mu.Lock()
		index := len(r.deliveries)
		r.deliveries = append(r.deliveries, delivery{
			body:       body,
			signature:  req.Header.Get(webhook.HeaderSignature),
			timestamp:  req.Header.Get(webhook.HeaderTimestamp),
			deliveryID: req.Header.Get(webhook.HeaderDelivery),
		})
		status := http.StatusOK
		if index < len(r.statuses) {
			status = r.statuses[index]
		} else if len(r.statuses) > 0 {
			status = r.statuses[len(r.statuses)-1]
		}
		r.mu.Unlock()

		w.WriteHeader(status)
	}))
	t.Cleanup(r.server.Close)
	return r
}

func (r *receiver) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.deliveries)
}

func (r *receiver) first() delivery {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.deliveries[0]
}

func finishedJob(url string) jobs.Job {
	duration := int32(4200)
	completed := time.Now().UTC()
	return jobs.Job{
		ID: uuid.New(), TenantID: uuid.New(), Status: jobs.StatusCompleted,
		TotalChars: 120, DurationMS: &duration, WebhookURL: url,
		Metadata:    map[string]string{"campaign": "spring", "_body_sha256": "secret-bookkeeping"},
		CompletedAt: &completed,
	}
}

func dispatcher(t *testing.T) *webhook.Dispatcher {
	t.Helper()

	d := webhook.NewDispatcher(webhookSecret, allowAllGuard{}, zerolog.New(io.Discard))
	d.SetBackoffForTest(func(context.Context, time.Duration) error { return nil })
	return d
}

func waitFor(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	require.Eventually(t, cond, 5*time.Second, 10*time.Millisecond, msg)
}

// US-15 acceptance criterion 1.
func TestWebhookIsSignedOverTimestampAndBody(t *testing.T) {
	t.Parallel()

	rx := newReceiver(t, http.StatusOK)
	job := finishedJob(rx.server.URL)

	dispatcher(t).JobFinished(context.Background(), job)
	waitFor(t, func() bool { return rx.count() == 1 }, "webhook never arrived")

	got := rx.first()
	require.True(t,
		webhook.VerifySignature([]byte(webhookSecret), got.timestamp, got.body, got.signature),
		"the receiver must be able to verify what we sent")
	require.True(t, strings.HasPrefix(got.signature, "sha256="))
	require.NotEmpty(t, got.timestamp)
	require.Contains(t, got.deliveryID, job.ID.String())
}

// A captured body replayed with a different timestamp must not verify.
func TestSignatureCoversTheTimestamp(t *testing.T) {
	t.Parallel()

	payload := []byte(`{"event":"job.completed"}`)
	signature := webhook.Signature([]byte(webhookSecret), "1700000000", payload)

	require.True(t, webhook.VerifySignature([]byte(webhookSecret), "1700000000", payload, signature))
	require.False(t, webhook.VerifySignature([]byte(webhookSecret), "1700009999", payload, signature))
}

func TestSignatureRejectsTheWrongSecret(t *testing.T) {
	t.Parallel()

	payload := []byte(`{"event":"job.completed"}`)
	signature := webhook.Signature([]byte(webhookSecret), "1700000000", payload)

	require.False(t, webhook.VerifySignature([]byte("another-secret"), "1700000000", payload, signature))
}

// US-15 acceptance criterion 2.
func TestWebhookRetriesUntilItSucceeds(t *testing.T) {
	t.Parallel()

	rx := newReceiver(t, http.StatusInternalServerError, http.StatusBadGateway, http.StatusOK)

	dispatcher(t).JobFinished(context.Background(), finishedJob(rx.server.URL))
	waitFor(t, func() bool { return rx.count() == 3 }, "webhook did not retry")

	time.Sleep(50 * time.Millisecond)
	require.Equal(t, 3, rx.count(), "delivery stops once the receiver accepts it")
}

func TestWebhookGivesUpAfterFiveAttempts(t *testing.T) {
	t.Parallel()

	rx := newReceiver(t, http.StatusInternalServerError)

	dispatcher(t).JobFinished(context.Background(), finishedJob(rx.server.URL))
	waitFor(t, func() bool { return rx.count() == webhook.MaxAttempts }, "wrong attempt count")

	time.Sleep(50 * time.Millisecond)
	require.Equal(t, webhook.MaxAttempts, rx.count(), "and does not keep trying forever")
}

// A 4xx is still retried: a receiver deploying badly for a minute should not lose its
// notification, and five attempts bound the cost either way.
func TestWebhookRetriesClientErrorsToo(t *testing.T) {
	t.Parallel()

	rx := newReceiver(t, http.StatusNotFound, http.StatusOK)

	dispatcher(t).JobFinished(context.Background(), finishedJob(rx.server.URL))
	waitFor(t, func() bool { return rx.count() == 2 }, "a 404 was not retried")
}

func TestWebhookPayloadHidesInternalMetadata(t *testing.T) {
	t.Parallel()

	rx := newReceiver(t, http.StatusOK)

	dispatcher(t).JobFinished(context.Background(), finishedJob(rx.server.URL))
	waitFor(t, func() bool { return rx.count() == 1 }, "webhook never arrived")

	body := string(rx.first().body)
	require.Contains(t, body, `"campaign":"spring"`)
	require.NotContains(t, body, "_body_sha256")
	require.NotContains(t, body, "secret-bookkeeping")
}

func TestNoWebhookUrlMeansNoDelivery(t *testing.T) {
	t.Parallel()

	rx := newReceiver(t, http.StatusOK)
	job := finishedJob("")

	dispatcher(t).JobFinished(context.Background(), job)

	time.Sleep(100 * time.Millisecond)
	require.Zero(t, rx.count())
}

// US-15 acceptance criterion 3: the guard runs again at delivery time, because DNS can
// change between job creation and completion.
func TestWebhookIsRecheckedBeforeEachAttempt(t *testing.T) {
	t.Parallel()

	rx := newReceiver(t, http.StatusOK)
	d := webhook.NewDispatcher(webhookSecret, rejectingGuard{}, zerolog.New(io.Discard))
	d.SetBackoffForTest(func(context.Context, time.Duration) error { return nil })

	d.JobFinished(context.Background(), finishedJob(rx.server.URL))

	time.Sleep(100 * time.Millisecond)
	require.Zero(t, rx.count(), "a URL that turned private must not be called")
}

type rejectingGuard struct{}

func (rejectingGuard) Check(context.Context, string) error { return jobs.ErrWebhookRejected }

// allowAllGuard stands in for the SSRF guard, whose own behaviour is tested in
// internal/jobs.
type allowAllGuard struct{}

func (allowAllGuard) Check(context.Context, string) error { return nil }
