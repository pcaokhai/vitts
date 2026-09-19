package usage_test

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/pcaokhai/vitts/gateway/internal/usage"
)

type recordingWriter struct {
	mu      sync.Mutex
	batches [][]usage.Record
	err     error
	block   chan struct{}
}

func (w *recordingWriter) WriteBatch(_ context.Context, records []usage.Record) error {
	if w.block != nil {
		<-w.block
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	if w.err != nil {
		return w.err
	}
	batch := make([]usage.Record, len(records))
	copy(batch, records)
	w.batches = append(w.batches, batch)
	return nil
}

func (w *recordingWriter) total() int {
	w.mu.Lock()
	defer w.mu.Unlock()

	var n int
	for _, batch := range w.batches {
		n += len(batch)
	}
	return n
}

func record() usage.Record {
	return usage.Record{
		TenantID: uuid.New(), KeyID: uuid.New(), VoiceID: "maichi",
		Mode: "sync", Chars: 42, Status: "ok",
	}
}

func TestMeterWritesWhatItWasGiven(t *testing.T) {
	t.Parallel()

	writer := &recordingWriter{}
	meter := usage.New(writer, zerolog.New(io.Discard))
	meter.Start()

	meter.Record(context.Background(), record())
	meter.Record(context.Background(), record())
	meter.Stop()

	require.Equal(t, 2, writer.total())
}

func TestMeterFillsIdentityAndTimestamp(t *testing.T) {
	t.Parallel()

	writer := &recordingWriter{}
	meter := usage.New(writer, zerolog.New(io.Discard))
	meter.Start()

	meter.Record(context.Background(), record())
	meter.Stop()

	written := writer.batches[0][0]
	require.NotEqual(t, uuid.Nil, written.ID, "every row needs an id for idempotent retries")
	require.False(t, written.CreatedAt.IsZero(), "created_at picks the partition")
	require.Equal(t, time.UTC, written.CreatedAt.Location(), "storage is UTC")
}

func TestMeterDrainsOnStop(t *testing.T) {
	t.Parallel()

	writer := &recordingWriter{}
	meter := usage.New(writer, zerolog.New(io.Discard))
	meter.Start()

	for range 50 {
		meter.Record(context.Background(), record())
	}
	meter.Stop()

	require.Equal(t, 50, writer.total(),
		"requests that happened must not be forgotten because we shut down")
}

// Metering is valuable, but not so valuable that a stalled database should stall
// synthesis: a full queue drops records and counts them.
func TestMeterNeverBlocksTheCaller(t *testing.T) {
	t.Parallel()

	writer := &recordingWriter{block: make(chan struct{})}
	meter := usage.New(writer, zerolog.New(io.Discard))
	meter.Start()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for range usage.QueueSize + 500 {
			meter.Record(context.Background(), record())
		}
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Record blocked on a stalled writer")
	}

	require.Positive(t, meter.Dropped(), "an overflowing queue must drop, not grow")

	close(writer.block)
	meter.Stop()
}

func TestMeterSurvivesAFailingWriter(t *testing.T) {
	t.Parallel()

	writer := &recordingWriter{err: errors.New("database down")}
	meter := usage.New(writer, zerolog.New(io.Discard))
	meter.Start()

	meter.Record(context.Background(), record())
	meter.Stop()

	writer.mu.Lock()
	defer writer.mu.Unlock()
	require.Empty(t, writer.batches, "the batch failed, and the meter kept running")
}
