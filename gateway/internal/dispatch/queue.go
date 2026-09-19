package dispatch

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
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
)

// Retry-After bounds from US-12 acceptance criterion 3.
const (
	MinRetryAfter = time.Second
	MaxRetryAfter = 30 * time.Second
)

// StreamReservePercent of slots are held for ClassStream, so saturated batch work cannot
// starve interactive traffic (ADR-007).
const StreamReservePercent = 30

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
	pool *Pool

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

// NewDispatcher wires the dispatcher to a pool.
func NewDispatcher(pool *Pool) *Dispatcher {
	return &Dispatcher{
		pool:        pool,
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

	d.enter(class)
	defer d.leave(class)

	for {
		if reservation, ok := d.tryReserve(class); ok {
			return reservation, nil
		}

		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, d.overload(class)
		}

		timer := time.NewTimer(remaining)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
			return nil, d.overload(class)
		case <-d.free:
			timer.Stop()
		}
	}
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
	started := time.Now()

	return &Reservation{
		Client: client,
		class:  class,
		release: func() {
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

	// Slots already reserved here are not yet visible in a worker's health snapshot,
	// which is up to one poll interval stale. Subtracting them prevents admitting the
	// same slot twice in that window.
	for _, held := range d.inFlight {
		free -= held
	}
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
	d.mu.Unlock()
}

func (d *Dispatcher) leave(class Class) {
	d.mu.Lock()
	d.waiting[class]--
	if d.waiting[class] < 0 {
		d.waiting[class] = 0
	}
	d.mu.Unlock()
}

// overload builds the refusal, including how long the caller should wait.
//
// Retry-After is queue depth times mean service time, bounded to 1-30 s (US-12
// acceptance criterion 3): unbounded values would park a client for minutes, and a
// missing one invites an immediate retry into the same refusal.
func (d *Dispatcher) overload(class Class) error {
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
