package dispatch

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/rs/zerolog"

	"github.com/pcaokhai/vitts/gateway/internal/telemetry"
)

// Class is a priority class (ADR-007). Streams are interactive and outrank sync
// requests, which outrank batch work from jobs.
type Class int

// The three classes, highest priority first.
const (
	ClassStream Class = iota
	ClassSync
	ClassBatch
)

// String makes the class readable in logs and metrics.
func (c Class) String() string {
	switch c {
	case ClassStream:
		return "stream"
	case ClassSync:
		return "sync"
	case ClassBatch:
		return "batch"
	default:
		return "unknown"
	}
}

// Queue wait budgets from ADR-007.
const (
	StreamWait = 2 * time.Second
	SyncWait   = 10 * time.Second
	// BatchWait is long rather than unbounded: "unbounded" in ADR-007 means batch yields
	// to higher classes, not that a job segment may wait forever holding a goroutine.
	BatchWait = 5 * time.Minute
	// recheckInterval bounds how long a waiter sleeps before re-reading capacity, so a
	// missed release signal costs a tick rather than the class budget. It runs only while
	// a caller is queued, which is already the exceptional path.
	recheckInterval = 50 * time.Millisecond
)

// Retry-After bounds from US-12 acceptance criterion 3.
const (
	MinRetryAfter = time.Second
	MaxRetryAfter = 30 * time.Second
)

// StreamReservePercent of slots are held for ClassStream, so saturated batch work cannot
// starve interactive traffic (ADR-007).
const StreamReservePercent = 30

// QueueDepthPerSlot bounds how many callers may wait per slot of fleet capacity.
//
// ADR-007 calls for a *bounded* queue per class, and the bound is what makes US-12's
// "p95 time to a 503 under 50 ms" achievable: past it a caller is refused in
// microseconds instead of waiting out the class budget first. Two per slot is about one
// service time of backlog, which is worth waiting for; more is not.
const QueueDepthPerSlot = 2

// MinQueueDepth keeps a single-slot fleet from refusing the very first waiter, which
// would turn a momentary overlap into an error.
const MinQueueDepth = 2

// ErrOverloaded means no slot became free within the class's wait budget. The caller
// returns 503 with Retry-After; it must never wait longer instead (NFR-05).
var ErrOverloaded = errors.New("overloaded")

// Reservation is a held dispatch slot. Release exactly once, in a defer.
type Reservation struct {
	Client  Client
	class   Class
	release func()
}

// Release returns the slot. Safe to call twice; the second call does nothing.
func (r *Reservation) Release() {
	if r.release != nil {
		r.release()
		r.release = nil
	}
}

// Dispatcher admits work to the worker fleet.
//
// It is the only place that decides whether a request runs now, waits, or is refused.
// Selection of *which* worker belongs to Pool; this decides *whether*.
type Dispatcher struct {
	pool    *Pool
	metrics *telemetry.Metrics
	logger  zerolog.Logger

	mu sync.Mutex
	// inFlight counts reservations currently held, by class.
	inFlight map[Class]int
	// waiting counts callers queued, by class, which drives Retry-After.
	waiting map[Class]int
	// serviceTime is the mean observed reservation lifetime, the other half of
	// Retry-After. It starts at a conservative guess and converges.
	serviceTime time.Duration
	// free is signalled whenever a slot is released, so waiters wake without polling.
	free chan struct{}

	waitFor func(Class) time.Duration
}

// initialServiceTime is the assumption before any request has completed. One second is
// roughly a short Vietnamese utterance at the measured RTF, so early Retry-After values
// are plausible rather than wild.
const initialServiceTime = time.Second

// NewDispatcher wires the dispatcher to a pool. Metrics may be nil in tests.
func NewDispatcher(pool *Pool, metrics *telemetry.Metrics, logger zerolog.Logger) *Dispatcher {
	return &Dispatcher{
		pool:        pool,
		metrics:     metrics,
		logger:      logger,
		inFlight:    make(map[Class]int),
		waiting:     make(map[Class]int),
		serviceTime: initialServiceTime,
		free:        make(chan struct{}, 1),
		waitFor:     defaultWait,
	}
}

func defaultWait(class Class) time.Duration {
	switch class {
	case ClassStream:
		return StreamWait
	case ClassSync:
		return SyncWait
	case ClassBatch:
		return BatchWait
	default:
		return SyncWait
	}
}

// Overload carries what a refused caller needs to retry sensibly.
type Overload struct {
	RetryAfter time.Duration
	Class      Class
	Waiting    int
}

// Error implements error so callers can errors.As it.
func (o *Overload) Error() string {
	return fmt.Sprintf("%s: class=%s retry_after=%s", ErrOverloaded, o.Class, o.RetryAfter)
}

// Unwrap lets errors.Is(err, ErrOverloaded) work.
func (o *Overload) Unwrap() error { return ErrOverloaded }

// Reserve takes a slot for one request.
//
// It returns as soon as a slot is free, or refuses when the class's budget expires. It
// never waits past that budget: accepting work we cannot start is the accept-then-timeout
// failure ADR-007 exists to prevent.
func (d *Dispatcher) Reserve(ctx context.Context, class Class) (*Reservation, error) {
	deadline := time.Now().Add(d.waitFor(class))
	queued := time.Now()

	// Refuse before queueing when this caller could not be served inside its budget
	// anyway. Admitting it would produce exactly the accept-then-timeout NFR-05 forbids:
	// the caller waits the full budget and still gets a 503, having learnt nothing it
	// could not have learnt immediately.
	if refuse, reason := d.shouldRefuse(class); refuse {
		d.logger.Debug().Str("class", class.String()).Str("reason", reason).
			Msg("refusing without queueing")
		return nil, d.overload(class)
	}

	d.enter(class)
	defer d.leave(class)

	// first guards the budget: a caller always gets one attempt, and every attempt after
	// that must still be inside its class budget. Without the guard a waiter woken by the
	// recheck tick could take a slot it was already too late for, which is the
	// accept-then-timeout NFR-05 forbids, arriving by the back door.
	for first := true; ; first = false {
		if !first && time.Now().After(deadline) {
			return nil, d.overload(class)
		}

		if reservation, ok := d.tryReserve(class); ok {
			d.observeWait(class, time.Since(queued))
			return reservation, nil
		}

		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, d.overload(class)
		}

		// Wake on the release signal, and also on a tick.
		//
		// The signal is an edge, but free capacity is derived from health snapshots that
		// the pool refreshes on its own schedule. A slot that frees without passing
		// through releaseSlot — a worker re-admitted after ejection, a request the worker
		// abandoned — moves the snapshot with no signal behind it, and a waiter parked on
		// the channel alone sleeps until its budget expires beside an idle worker. The
		// M3 drill caught exactly that: queue depth 1, worker_slots_busy 0
		// (docs/reports/drill-m3.md, finding 5).
		if remaining > recheckInterval {
			remaining = recheckInterval
		}
		timer := time.NewTimer(remaining)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		case <-d.free:
			timer.Stop()
		}
	}
}

// shouldRefuse decides whether to admit a caller to the queue at all.
//
// Two bounds apply. The queue is capped per class (ADR-007), and separately a caller is
// refused when the queue ahead of it cannot drain inside its class budget: with a
// measured mean service time, "depth ahead × service time" is how long this caller would
// actually wait, and admitting it past that point guarantees a slow 503 instead of a
// fast one (NFR-05, US-12 acceptance criterion 1).
func (d *Dispatcher) shouldRefuse(class Class) (bool, string) {
	d.mu.Lock()
	defer d.mu.Unlock()

	total, free := d.capacity()
	if free > 0 {
		return false, "" // a slot is available now
	}

	limit := total * QueueDepthPerSlot
	if limit < MinQueueDepth {
		limit = MinQueueDepth
	}
	depth := d.waiting[class]
	if depth >= limit {
		return true, "queue full"
	}

	// How long this caller would wait: everyone already queued, plus itself, divided by
	// the number of slots that will free up in parallel.
	slots := total
	if slots < 1 {
		slots = 1
	}
	expected := time.Duration((depth+1)/slots+1) * d.serviceTime
	if expected > d.waitFor(class) {
		return true, "expected wait exceeds the class budget"
	}

	return false, ""
}

// tryReserve takes a slot if the fleet has one this class may use.
func (d *Dispatcher) tryReserve(class Class) (*Reservation, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()

	capacityTotal, capacityFree := d.capacity()
	if capacityFree <= 0 {
		return nil, false
	}
	if class != ClassStream && capacityFree <= reservedForStream(capacityTotal) {
		// The remaining slots are the stream reservation; lower classes wait.
		return nil, false
	}

	client, err := d.pool.Pick()
	if err != nil {
		return nil, false
	}

	d.inFlight[class]++
	// Tell the pool immediately: its health snapshot will not show this slot as busy
	// until the next poll, and admitting against it twice is the failure this prevents.
	d.pool.NoteSlotTaken(client.Addr)
	started := time.Now()

	return &Reservation{
		Client: client,
		class:  class,
		release: func() {
			d.pool.NoteSlotFreed(client.Addr)
			d.releaseSlot(class, time.Since(started))
		},
	}, true
}

// capacity reports the fleet's total and free slots from the last health snapshots.
func (d *Dispatcher) capacity() (total, free int) {
	for _, snap := range d.pool.Snapshots() {
		if !snap.Ready || snap.Ejected {
			continue
		}
		total += int(snap.SlotsTotal)
		free += int(snap.Free())
	}

	// No adjustment for in-flight work: the pool is told as slots are taken and freed,
	// so the snapshot already accounts for what this gateway holds.
	if free < 0 {
		free = 0
	}
	return total, free
}

func reservedForStream(total int) int {
	return total * StreamReservePercent / 100
}

func (d *Dispatcher) releaseSlot(class Class, elapsed time.Duration) {
	d.mu.Lock()
	d.inFlight[class]--
	if d.inFlight[class] < 0 {
		d.inFlight[class] = 0
	}
	// Exponential moving average: recent requests describe current load better than the
	// whole history, and Retry-After should follow load.
	d.serviceTime = (d.serviceTime*3 + elapsed) / 4
	d.mu.Unlock()

	// Non-blocking: a waiter that is already awake does not need another signal.
	select {
	case d.free <- struct{}{}:
	default:
	}
}

func (d *Dispatcher) enter(class Class) {
	d.mu.Lock()
	d.waiting[class]++
	depth := d.waiting[class]
	d.mu.Unlock()

	if d.metrics != nil {
		d.metrics.QueueDepth.WithLabelValues(class.String()).Set(float64(depth))
	}
}

func (d *Dispatcher) observeWait(class Class, waited time.Duration) {
	if d.metrics != nil {
		d.metrics.QueueWaitSeconds.WithLabelValues(class.String()).Observe(waited.Seconds())
	}
}

func (d *Dispatcher) leave(class Class) {
	d.mu.Lock()
	d.waiting[class]--
	if d.waiting[class] < 0 {
		d.waiting[class] = 0
	}
	depth := d.waiting[class]
	d.mu.Unlock()

	if d.metrics != nil {
		d.metrics.QueueDepth.WithLabelValues(class.String()).Set(float64(depth))
	}
}

// overload builds the refusal, including how long the caller should wait.
//
// Retry-After is queue depth times mean service time, bounded to 1-30 s (US-12
// acceptance criterion 3): unbounded values would park a client for minutes, and a
// missing one invites an immediate retry into the same refusal.
func (d *Dispatcher) overload(class Class) error {
	if d.metrics != nil {
		d.metrics.Overloaded.WithLabelValues(class.String()).Inc()
	}

	d.mu.Lock()
	waiting := d.waiting[class]
	total, _ := d.capacity()
	service := d.serviceTime
	d.mu.Unlock()

	perSlot := waiting
	if total > 0 {
		perSlot = (waiting + total - 1) / total
	}

	retry := time.Duration(perSlot) * service
	switch {
	case retry < MinRetryAfter:
		retry = MinRetryAfter
	case retry > MaxRetryAfter:
		retry = MaxRetryAfter
	}

	return &Overload{RetryAfter: retry, Class: class, Waiting: waiting}
}

// Stats is the dispatcher's view of itself, for metrics and tests.
type Stats struct {
	InFlight    map[Class]int
	Waiting     map[Class]int
	ServiceTime time.Duration
	FreeSlots   int
	TotalSlots  int
}

// Stats snapshots the dispatcher.
func (d *Dispatcher) Stats() Stats {
	d.mu.Lock()
	defer d.mu.Unlock()

	total, free := d.capacity()
	stats := Stats{
		InFlight:    make(map[Class]int, len(d.inFlight)),
		Waiting:     make(map[Class]int, len(d.waiting)),
		ServiceTime: d.serviceTime,
		FreeSlots:   free,
		TotalSlots:  total,
	}
	for class, n := range d.inFlight {
		stats.InFlight[class] = n
	}
	for class, n := range d.waiting {
		stats.Waiting[class] = n
	}
	return stats
}
