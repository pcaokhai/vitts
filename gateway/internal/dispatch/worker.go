// Package dispatch owns the pool of inference workers: their health, their selection and
// the circuit that keeps a sick worker out of rotation.
//
// It is an outbound adapter. Use cases ask it for a worker; they never dial gRPC
// themselves (.claude/rules/gateway-go.md).
package dispatch

import (
	"context"
	"fmt"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	workerpb "github.com/pcaokhai/vitts/gateway/internal/gen/workerpb"
)

// Timings from docs/06-flows.md FL-06.
const (
	// HealthInterval is how often a healthy worker is re-probed.
	HealthInterval = 5 * time.Second
	// ProbeInterval is how often an ejected worker is re-probed. Slower, because an
	// ejected worker is usually restarting and hammering it does not help.
	ProbeInterval = 30 * time.Second
	// FailureThreshold is how many consecutive failures eject a worker.
	FailureThreshold = 3
	// healthTimeout bounds one probe. A worker that cannot answer Health in this window
	// is not fit to take a synthesis request either.
	healthTimeout = 2 * time.Second
)

// Snapshot is the last known state of one worker.
type Snapshot struct {
	Addr         string
	Ready        bool
	ModelVersion string
	SlotsTotal   int32
	SlotsBusy    int32
	RTFEWMA      float32
	VoiceIDs     []string
	// Ejected is true once the circuit opened; the worker is re-probed on ProbeInterval.
	Ejected bool
	// Failures is the consecutive failure count, reset by any success.
	Failures int
	LastSeen time.Time
}

// Free is the number of idle slots this worker reports.
func (s Snapshot) Free() int32 {
	free := s.SlotsTotal - s.SlotsBusy
	if free < 0 {
		return 0
	}
	return free
}

// Selectable reports whether the worker may receive a request right now.
func (s Snapshot) Selectable() bool {
	return s.Ready && !s.Ejected && s.Free() > 0
}

// worker is one pool member: its connection, its client and its mutable state.
type worker struct {
	addr   string
	conn   *grpc.ClientConn
	client workerpb.WorkerClient
	opts   Options

	mu    sync.RWMutex
	state Snapshot
	// lastProbe is when a probe last ran, successful or not. Scheduling uses it instead
	// of Snapshot.LastSeen, which only moves on success and would otherwise leave a
	// failing worker permanently overdue and spinning the poll loop.
	lastProbe time.Time
}

func newWorker(addr string, opts Options) (*worker, error) {
	// Lazy dial: NewClient does not block, so one unreachable worker cannot stop the
	// gateway from booting. Health polling decides when it is usable.
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", addr, err)
	}

	return &worker{
		addr:   addr,
		conn:   conn,
		client: workerpb.NewWorkerClient(conn),
		opts:   opts,
		state:  Snapshot{Addr: addr},
	}, nil
}

// probe runs one Health RPC and folds the result into the worker's state.
func (w *worker) probe(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, healthTimeout)
	defer cancel()

	reply, err := w.client.Health(ctx, &workerpb.HealthRequest{})

	w.mu.Lock()
	defer w.mu.Unlock()
	w.lastProbe = time.Now()

	if err != nil {
		w.state.Failures++
		w.state.Ready = false
		if w.state.Failures >= w.opts.FailureThreshold {
			w.state.Ejected = true
		}
		return
	}

	w.state = Snapshot{
		Addr:         w.addr,
		Ready:        reply.GetReady(),
		ModelVersion: reply.GetModelVersion(),
		SlotsTotal:   reply.GetSlotsTotal(),
		SlotsBusy:    reply.GetSlotsBusy(),
		RTFEWMA:      reply.GetRtfEwma(),
		VoiceIDs:     reply.GetVoiceIds(),
		LastSeen:     time.Now(),
	}
}

func (w *worker) snapshot() Snapshot {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.state
}

// dueAt reports when this worker should next be probed.
func (w *worker) dueAt(now time.Time) time.Time {
	w.mu.RLock()
	defer w.mu.RUnlock()

	if w.lastProbe.IsZero() {
		return now
	}
	interval := w.opts.HealthInterval
	if w.state.Ejected {
		interval = w.opts.ProbeInterval
	}
	return w.lastProbe.Add(interval)
}

func (w *worker) close() error {
	if err := w.conn.Close(); err != nil {
		return fmt.Errorf("close %s: %w", w.addr, err)
	}
	return nil
}
