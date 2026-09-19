// Package usage records what each request consumed.
//
// Writes are batched and asynchronous: metering must never add latency to the request it
// measures, and a slow database must not become a slow API.
package usage

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

// Record is one metered request, matching the synth_requests row.
type Record struct {
	ID         uuid.UUID
	TenantID   uuid.UUID
	KeyID      uuid.UUID
	VoiceID    string
	CacheKey   []byte
	Mode       string
	Chars      int64
	DurationMS int32
	TTFAMS     int32
	Cached     bool
	Status     string
	ErrorCode  string
	CreatedAt  time.Time
}

// Writer persists a batch. Implemented by the Postgres adapter.
type Writer interface {
	WriteBatch(ctx context.Context, records []Record) error
}

// Tuning. The queue is bounded because everything in this gateway is bounded: under a
// flood it is better to lose metering than to grow memory until the process dies.
const (
	QueueSize     = 4096
	BatchSize     = 128
	FlushInterval = 2 * time.Second
	writeTimeout  = 10 * time.Second
)

// Meter buffers records and writes them in batches.
type Meter struct {
	writer  Writer
	logger  zerolog.Logger
	queue   chan Record
	done    chan struct{}
	wg      sync.WaitGroup
	dropped atomic64
}

// New builds a meter. Start must be called once.
func New(writer Writer, logger zerolog.Logger) *Meter {
	return &Meter{
		writer: writer,
		logger: logger,
		queue:  make(chan Record, QueueSize),
		done:   make(chan struct{}),
	}
}

// Record enqueues one record.
//
// It never blocks: a full queue drops the record and counts it. Metering is valuable,
// but not so valuable that a database stall should stall synthesis.
func (m *Meter) Record(_ context.Context, record Record) {
	if record.ID == uuid.Nil {
		record.ID = uuid.New()
	}
	if record.CreatedAt.IsZero() {
		record.CreatedAt = time.Now().UTC()
	}

	select {
	case m.queue <- record:
	default:
		if dropped := m.dropped.add(1); dropped%100 == 1 {
			m.logger.Error().Int64("dropped", dropped).Msg("usage queue full, records dropped")
		}
	}
}

// Dropped is how many records were discarded because the queue was full.
func (m *Meter) Dropped() int64 { return m.dropped.load() }

// Start runs the batch writer until Stop.
func (m *Meter) Start() {
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()

		ticker := time.NewTicker(FlushInterval)
		defer ticker.Stop()

		batch := make([]Record, 0, BatchSize)
		for {
			select {
			case record := <-m.queue:
				batch = append(batch, record)
				if len(batch) >= BatchSize {
					batch = m.flush(batch)
				}
			case <-ticker.C:
				batch = m.flush(batch)
			case <-m.done:
				// Drain what is already queued before leaving: those requests really
				// happened, and shutdown is not a reason to forget them.
				for {
					select {
					case record := <-m.queue:
						batch = append(batch, record)
						if len(batch) >= BatchSize {
							batch = m.flush(batch)
						}
						continue
					default:
					}
					break
				}
				m.flush(batch)
				return
			}
		}
	}()
}

// Stop drains the queue and waits for the writer to finish.
func (m *Meter) Stop() {
	close(m.done)
	m.wg.Wait()
}

// flush writes a batch and returns an empty one to reuse.
func (m *Meter) flush(batch []Record) []Record {
	if len(batch) == 0 {
		return batch
	}

	// Detached from any request context: a cancelled request's own metering must still
	// be written.
	ctx, cancel := context.WithTimeout(context.Background(), writeTimeout)
	defer cancel()

	if err := m.writer.WriteBatch(ctx, batch); err != nil {
		m.logger.Error().Err(err).Int("records", len(batch)).Msg("usage batch not written")
	}

	return batch[:0]
}

// atomic64 is a tiny wrapper so the dropped counter needs no external dependency.
type atomic64 struct {
	mu sync.Mutex
	n  int64
}

func (a *atomic64) add(delta int64) int64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.n += delta
	return a.n
}

func (a *atomic64) load() int64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.n
}
