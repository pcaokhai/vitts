package dispatch_test

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/pcaokhai/vitts/gateway/internal/dispatch"
	"github.com/pcaokhai/vitts/gateway/internal/dispatch/fakeworker"
	workerpb "github.com/pcaokhai/vitts/gateway/internal/gen/workerpb"
)

// dispatcherWith starts a pool whose single worker advertises slotsTotal slots.
func dispatcherWith(t *testing.T, slots int32) (*dispatch.Dispatcher, *fakeworker.Server) {
	t.Helper()

	servers := startWorkers(t, 1)
	servers[0].SetHealth(&workerpb.HealthResponse{
		Ready: true, ModelVersion: "test", SlotsTotal: slots, SlotsBusy: 0,
	})
	pool := startPool(t, servers...)
	eventually(t, func() bool { return pool.Ready(context.Background()) == nil }, "pool never ready")

	return dispatch.NewDispatcher(pool, nil, zerolog.New(io.Discard)), servers[0]
}

func TestReserveSucceedsWhenTheFleetHasCapacity(t *testing.T) {
	t.Parallel()

	d, _ := dispatcherWith(t, 4)

	reservation, err := d.Reserve(context.Background(), dispatch.ClassStream)

	require.NoError(t, err)
	require.NotNil(t, reservation.Client.Worker)
	reservation.Release()
}

func TestReserveRefusesFastWhenSaturated(t *testing.T) {
	t.Parallel()

	d, _ := dispatcherWith(t, 1)
	held, err := d.Reserve(context.Background(), dispatch.ClassStream)
	require.NoError(t, err)
	t.Cleanup(held.Release)

	// Stream's budget is 2s; the refusal itself must be immediate once it arrives.
	d.SetWaitForTest(func(dispatch.Class) time.Duration { return 10 * time.Millisecond })

	started := time.Now()
	_, err = d.Reserve(context.Background(), dispatch.ClassStream)
	elapsed := time.Since(started)

	require.ErrorIs(t, err, dispatch.ErrOverloaded)
	require.Less(t, elapsed, 100*time.Millisecond,
		"overload must be refused, never accepted and timed out (ADR-007)")
}

func TestOverloadCarriesABoundedRetryAfter(t *testing.T) {
	t.Parallel()

	d, _ := dispatcherWith(t, 1)
	held, err := d.Reserve(context.Background(), dispatch.ClassSync)
	require.NoError(t, err)
	t.Cleanup(held.Release)
	d.SetWaitForTest(func(dispatch.Class) time.Duration { return 10 * time.Millisecond })

	_, err = d.Reserve(context.Background(), dispatch.ClassSync)

	var overload *dispatch.Overload
	require.ErrorAs(t, err, &overload)
	require.GreaterOrEqual(t, overload.RetryAfter, dispatch.MinRetryAfter)
	require.LessOrEqual(t, overload.RetryAfter, dispatch.MaxRetryAfter)
	require.Equal(t, dispatch.ClassSync, overload.Class)
}

// ADR-007: 30% of slots are held for streams so batch cannot starve interactive traffic.
func TestBatchCannotConsumeTheStreamReservation(t *testing.T) {
	t.Parallel()

	d, _ := dispatcherWith(t, 10) // 30% reserved = 3 slots
	d.SetWaitForTest(func(dispatch.Class) time.Duration { return 10 * time.Millisecond })

	var held []*dispatch.Reservation
	for range 7 {
		reservation, err := d.Reserve(context.Background(), dispatch.ClassBatch)
		require.NoError(t, err)
		held = append(held, reservation)
	}
	t.Cleanup(func() {
		for _, r := range held {
			r.Release()
		}
	})

	_, err := d.Reserve(context.Background(), dispatch.ClassBatch)
	require.ErrorIs(t, err, dispatch.ErrOverloaded, "batch stops at the reservation line")

	stream, err := d.Reserve(context.Background(), dispatch.ClassStream)
	require.NoError(t, err, "a stream may use the reserved slots")
	stream.Release()
}

// T-109. Capacity can also return without a release: a worker re-admitted after ejection,
// or one that abandoned a request, moves the health snapshot with no signal behind it. A
// waiter parked on the release channel alone slept beside an idle worker until its budget
// expired — in the M3 drill, a job queued for minutes while worker_slots_busy read 0
// (docs/reports/drill-m3.md, finding 5).
func TestAWaiterIsAdmittedWhenCapacityReturnsWithoutARelease(t *testing.T) {
	t.Parallel()

	d, server := dispatcherWith(t, 1)
	busy := &workerpb.HealthResponse{
		Ready: true, ModelVersion: "test", SlotsTotal: 1, SlotsBusy: 1,
	}
	server.SetHealth(busy)
	// Wait for the dispatcher to actually see a full fleet, or the waiter below never
	// waits and the test proves nothing.
	eventually(t, func() bool { return d.FreeSlotsForTest() == 0 }, "fleet never looked busy")

	admitted := make(chan error, 1)
	go func() {
		reservation, err := d.Reserve(context.Background(), dispatch.ClassStream)
		if err == nil {
			reservation.Release()
		}
		admitted <- err
	}()

	time.Sleep(50 * time.Millisecond) // let the waiter park
	server.SetHealth(&workerpb.HealthResponse{
		Ready: true, ModelVersion: "test", SlotsTotal: 1, SlotsBusy: 0,
	})

	select {
	case err := <-admitted:
		require.NoError(t, err, "a free slot must admit the waiter even with no release signal")
	case <-time.After(dispatch.StreamWait):
		t.Fatal("waiter slept beside an idle worker until its budget expired")
	}
}

func TestReleasingASlotWakesAWaiter(t *testing.T) {
	t.Parallel()

	d, _ := dispatcherWith(t, 1)
	held, err := d.Reserve(context.Background(), dispatch.ClassStream)
	require.NoError(t, err)

	admitted := make(chan error, 1)
	go func() {
		reservation, err := d.Reserve(context.Background(), dispatch.ClassStream)
		if err == nil {
			reservation.Release()
		}
		admitted <- err
	}()

	time.Sleep(20 * time.Millisecond) // let the waiter queue
	held.Release()

	select {
	case err := <-admitted:
		require.NoError(t, err, "a freed slot must admit the waiter, not leave it to time out")
	case <-time.After(dispatch.StreamWait):
		t.Fatal("waiter was never admitted")
	}
}

func TestReserveHonoursContextCancellation(t *testing.T) {
	t.Parallel()

	d, _ := dispatcherWith(t, 1)
	held, err := d.Reserve(context.Background(), dispatch.ClassStream)
	require.NoError(t, err)
	t.Cleanup(held.Release)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = d.Reserve(ctx, dispatch.ClassStream)

	require.True(t, errors.Is(err, context.Canceled),
		"a client that hung up must not keep a queue slot")
}

func TestReleaseIsIdempotent(t *testing.T) {
	t.Parallel()

	d, _ := dispatcherWith(t, 2)
	reservation, err := d.Reserve(context.Background(), dispatch.ClassStream)
	require.NoError(t, err)

	reservation.Release()
	reservation.Release()

	require.Zero(t, d.Stats().InFlight[dispatch.ClassStream])
}

func TestStatsReportQueueState(t *testing.T) {
	t.Parallel()

	d, _ := dispatcherWith(t, 4)
	reservation, err := d.Reserve(context.Background(), dispatch.ClassStream)
	require.NoError(t, err)
	t.Cleanup(reservation.Release)

	stats := d.Stats()

	require.Equal(t, 1, stats.InFlight[dispatch.ClassStream])
	require.Equal(t, 4, stats.TotalSlots)
	require.Equal(t, 3, stats.FreeSlots, "a held reservation is not free, even before the next health poll")
}

func TestNoReadyWorkerIsOverloadNotAPanic(t *testing.T) {
	t.Parallel()

	servers := startWorkers(t, 1)
	servers[0].SetHealth(&workerpb.HealthResponse{Ready: false})
	pool := startPool(t, servers...)
	d := dispatch.NewDispatcher(pool, nil, zerolog.New(io.Discard))
	d.SetWaitForTest(func(dispatch.Class) time.Duration { return 10 * time.Millisecond })

	_, err := d.Reserve(context.Background(), dispatch.ClassStream)

	require.ErrorIs(t, err, dispatch.ErrOverloaded)
}

func TestClassNames(t *testing.T) {
	t.Parallel()

	require.Equal(t, "stream", dispatch.ClassStream.String())
	require.Equal(t, "sync", dispatch.ClassSync.String())
	require.Equal(t, "batch", dispatch.ClassBatch.String())
}

// ADR-007 calls for a bounded queue, and US-12 measures what the bound buys: once the
// queue ahead of a caller cannot drain inside its class budget, the caller is refused in
// microseconds rather than after waiting the budget out for the same answer.
func TestAnOverloadedQueueRefusesImmediately(t *testing.T) {
	t.Parallel()

	d, _ := dispatcherWith(t, 1)
	held, err := d.Reserve(context.Background(), dispatch.ClassStream)
	require.NoError(t, err)
	t.Cleanup(held.Release)

	// Park callers until the dispatcher stops admitting them.
	parked, cancel := context.WithCancel(context.Background())
	defer cancel()
	for range 4 {
		go func() {
			if reservation, err := d.Reserve(parked, dispatch.ClassStream); err == nil {
				reservation.Release()
			}
		}()
	}
	require.Eventually(t, func() bool {
		return d.Stats().Waiting[dispatch.ClassStream] > 0
	}, 2*time.Second, 5*time.Millisecond, "nobody queued at all")

	// Whatever the admitted depth is, the next caller must be told at once.
	fastest := time.Hour
	for range 5 {
		started := time.Now()
		_, err := d.Reserve(context.Background(), dispatch.ClassStream)
		elapsed := time.Since(started)
		if errors.Is(err, dispatch.ErrOverloaded) && elapsed < fastest {
			fastest = elapsed
		}
	}

	require.Less(t, fastest, 50*time.Millisecond,
		"an overloaded dispatcher must refuse in microseconds, not after the 2 s budget")
}

// The rule must not make the dispatcher paranoid: while a slot is free, callers are
// served rather than refused.
func TestAvailableCapacityIsAlwaysAdmitted(t *testing.T) {
	t.Parallel()

	d, _ := dispatcherWith(t, 4)

	var held []*dispatch.Reservation
	for range 4 {
		reservation, err := d.Reserve(context.Background(), dispatch.ClassStream)
		require.NoError(t, err, "a free slot must never be refused")
		held = append(held, reservation)
	}
	for _, r := range held {
		r.Release()
	}
}
