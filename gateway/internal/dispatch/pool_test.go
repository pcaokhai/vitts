package dispatch_test

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/pcaokhai/vitts/gateway/internal/dispatch"
	"github.com/pcaokhai/vitts/gateway/internal/dispatch/fakeworker"
	workerpb "github.com/pcaokhai/vitts/gateway/internal/gen/workerpb"
)

// eventually polls until cond holds, so tests wait on the pool's own schedule rather than
// on a fixed sleep.
func eventually(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	require.Eventually(t, cond, 5*time.Second, 20*time.Millisecond, msg)
}

// fastOptions keeps the FL-06 failure threshold, which is behaviour, and shortens only
// the intervals, which are tuning.
var fastOptions = dispatch.Options{
	HealthInterval: 10 * time.Millisecond,
	ProbeInterval:  20 * time.Millisecond,
	Tick:           5 * time.Millisecond,
}

func startWorkers(t *testing.T, n int) []*fakeworker.Server {
	t.Helper()

	servers := make([]*fakeworker.Server, 0, n)
	for range n {
		server, err := fakeworker.Start()
		require.NoError(t, err)
		t.Cleanup(server.Stop)
		servers = append(servers, server)
	}
	return servers
}

func startPool(t *testing.T, servers ...*fakeworker.Server) *dispatch.Pool {
	t.Helper()

	addrs := make([]string, 0, len(servers))
	for _, s := range servers {
		addrs = append(addrs, s.Addr())
	}

	// Production timings are seconds; the tests drive the same state machine faster.
	pool, err := dispatch.NewPool(addrs, zerolog.New(io.Discard), fastOptions, nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, pool.Close()) })

	pool.Start(context.Background())
	return pool
}

func TestPoolNeedsAtLeastOneAddress(t *testing.T) {
	t.Parallel()

	_, err := dispatch.NewPool(nil, zerolog.New(io.Discard), dispatch.Options{}, nil)

	require.Error(t, err)
}

func TestPoolBootsWithAnUnreachableWorker(t *testing.T) {
	t.Parallel()

	// A gateway must start and report itself unready, not refuse to boot (FL-06).
	pool, err := dispatch.NewPool([]string{"127.0.0.1:1"}, zerolog.New(io.Discard), fastOptions, nil)

	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, pool.Close()) })
	require.Error(t, pool.Ready(context.Background()))
}

func TestPoolBecomesReadyWhenAWorkerIsReady(t *testing.T) {
	t.Parallel()

	pool := startPool(t, startWorkers(t, 1)...)

	eventually(t, func() bool { return pool.Ready(context.Background()) == nil },
		"pool never saw a ready worker")

	client, err := pool.Pick()
	require.NoError(t, err)
	require.Equal(t, "test", client.ModelVersion)
	require.NotNil(t, client.Worker)
}

func TestLoadingWorkerIsNotSelectable(t *testing.T) {
	t.Parallel()

	servers := startWorkers(t, 1)
	servers[0].SetHealth(&workerpb.HealthResponse{Ready: false, SlotsTotal: 1})
	pool := startPool(t, servers...)

	eventually(t, func() bool { return servers[0].Probes() > 0 }, "no probe reached the worker")

	_, err := pool.Pick()
	require.ErrorIs(t, err, dispatch.ErrNoWorker)
	require.Error(t, pool.Ready(context.Background()))
}

func TestBusyWorkerIsNotSelectable(t *testing.T) {
	t.Parallel()

	servers := startWorkers(t, 1)
	servers[0].SetHealth(&workerpb.HealthResponse{
		Ready: true, ModelVersion: "test", SlotsTotal: 1, SlotsBusy: 1,
	})
	pool := startPool(t, servers...)

	eventually(t, func() bool { return servers[0].Probes() > 0 }, "no probe reached the worker")

	_, err := pool.Pick()
	require.ErrorIs(t, err, dispatch.ErrNoWorker)

	// Ready is about the fleet being alive, not about spare capacity: a fully busy
	// worker still means the gateway can serve once a slot frees.
	require.NoError(t, pool.Ready(context.Background()))
}

func TestPickPrefersTheWorkerWithMoreFreeSlots(t *testing.T) {
	t.Parallel()

	servers := startWorkers(t, 2)
	servers[0].SetHealth(&workerpb.HealthResponse{
		Ready: true, ModelVersion: "busy", SlotsTotal: 4, SlotsBusy: 3,
	})
	servers[1].SetHealth(&workerpb.HealthResponse{
		Ready: true, ModelVersion: "idle", SlotsTotal: 4, SlotsBusy: 1,
	})
	pool := startPool(t, servers...)

	eventually(t, func() bool {
		client, err := pool.Pick()
		return err == nil && client.ModelVersion == "idle"
	}, "pick never settled on the least-busy worker")
}

func TestPickBreaksTiesOnRTF(t *testing.T) {
	t.Parallel()

	servers := startWorkers(t, 2)
	servers[0].SetHealth(&workerpb.HealthResponse{
		Ready: true, ModelVersion: "slow", SlotsTotal: 2, SlotsBusy: 0, RtfEwma: 0.9,
	})
	servers[1].SetHealth(&workerpb.HealthResponse{
		Ready: true, ModelVersion: "fast", SlotsTotal: 2, SlotsBusy: 0, RtfEwma: 0.3,
	})
	pool := startPool(t, servers...)

	eventually(t, func() bool {
		client, err := pool.Pick()
		return err == nil && client.ModelVersion == "fast"
	}, "equally idle workers were not ordered by RTF")
}

func TestWorkerIsEjectedAfterThreeConsecutiveFailures(t *testing.T) {
	t.Parallel()

	servers := startWorkers(t, 1)
	pool := startPool(t, servers...)
	eventually(t, func() bool { return pool.Ready(context.Background()) == nil }, "never became ready")

	servers[0].SetFailing(true)

	eventually(t, func() bool {
		snap := pool.Snapshots()[0]
		return snap.Ejected && snap.Failures >= dispatch.FailureThreshold
	}, "worker was never ejected")

	_, err := pool.Pick()
	require.ErrorIs(t, err, dispatch.ErrNoWorker)
}

func TestEjectedWorkerRecoversOnASuccessfulProbe(t *testing.T) {
	t.Parallel()

	servers := startWorkers(t, 1)
	pool := startPool(t, servers...)
	servers[0].SetFailing(true)
	eventually(t, func() bool { return pool.Snapshots()[0].Ejected }, "never ejected")

	servers[0].SetFailing(false)

	// The re-probe interval is 30s in production; this asserts the state machine, not
	// the wall clock, by driving a probe directly through the pool's own schedule.
	eventually(t, func() bool {
		snap := pool.Snapshots()[0]
		return !snap.Ejected && snap.Ready
	}, "worker never recovered")
}

func TestFleetSurvivesOneSickWorker(t *testing.T) {
	t.Parallel()

	servers := startWorkers(t, 2)
	pool := startPool(t, servers...)
	eventually(t, func() bool { return pool.Ready(context.Background()) == nil }, "never ready")

	servers[0].SetFailing(true)

	eventually(t, func() bool { return pool.Snapshots()[0].Ejected }, "sick worker never ejected")
	require.NoError(t, pool.Ready(context.Background()), "a healthy worker remains")

	client, err := pool.Pick()
	require.NoError(t, err)
	require.Equal(t, servers[1].Addr(), client.Addr)
}

func TestCloseIsIdempotent(t *testing.T) {
	t.Parallel()

	servers := startWorkers(t, 1)
	addrs := []string{servers[0].Addr()}
	pool, err := dispatch.NewPool(addrs, zerolog.New(io.Discard), fastOptions, nil)
	require.NoError(t, err)
	pool.Start(context.Background())

	require.NoError(t, pool.Close())
	require.NoError(t, pool.Close())
}
