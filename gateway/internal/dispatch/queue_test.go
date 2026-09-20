package dispatch_test

import (
	"context"
	"errors"
	"testing"
	"time"

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

	return dispatch.NewDispatcher(pool, nil), servers[0]
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
	d := dispatch.NewDispatcher(pool, nil)
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
