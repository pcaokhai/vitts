package dispatch

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/rs/zerolog"

	workerpb "github.com/pcaokhai/vitts/gateway/internal/gen/workerpb"
)

// ErrNoWorker means no worker can take a request right now: all are ejected, still
// loading, or busy. The caller turns it into 503 with Retry-After (ADR-007); it must
// never be turned into a queue here, because queueing is task 1.7's decision.
var ErrNoWorker = errors.New("no worker available")

// Options tunes the health machinery. The zero value means the FL-06 defaults; tests
// shorten them to drive the state machine without waiting on the wall clock.
type Options struct {
	HealthInterval   time.Duration
	ProbeInterval    time.Duration
	FailureThreshold int
	// Tick is the poll loop's cadence. Individual workers are due on their own schedule;
	// this only bounds how precisely that schedule is honoured.
	Tick time.Duration
}

func (o Options) withDefaults() Options {
	if o.HealthInterval <= 0 {
		o.HealthInterval = HealthInterval
	}
	if o.ProbeInterval <= 0 {
		o.ProbeInterval = ProbeInterval
	}
	if o.FailureThreshold <= 0 {
		o.FailureThreshold = FailureThreshold
	}
	if o.Tick <= 0 {
		o.Tick = time.Second
	}
	return o
}

// Pool keeps the fleet's health current and hands out the least-busy worker.
type Pool struct {
	workers []*worker
	logger  zerolog.Logger
	opts    Options

	stop     chan struct{}
	stopped  sync.WaitGroup
	stopOnce sync.Once
	closeErr error
}

// NewPool dials every address. It does not wait for any worker to be ready: a gateway
// must boot and report itself unready rather than refuse to start (FL-06).
func NewPool(addrs []string, logger zerolog.Logger, opts Options) (*Pool, error) {
	if len(addrs) == 0 {
		return nil, errors.New("no worker addresses configured")
	}
	opts = opts.withDefaults()

	workers := make([]*worker, 0, len(addrs))
	for _, addr := range addrs {
		w, err := newWorker(addr, opts)
		if err != nil {
			for _, opened := range workers {
				_ = opened.close()
			}
			return nil, err
		}
		workers = append(workers, w)
	}

	return &Pool{
		workers: workers,
		logger:  logger,
		opts:    opts,
		stop:    make(chan struct{}),
	}, nil
}

// Start runs health polling until Close. It returns immediately.
func (p *Pool) Start(ctx context.Context) {
	p.stopped.Add(1)
	go func() {
		defer p.stopped.Done()

		// Probe once up front so /readyz reflects reality within a second of boot
		// rather than after a full interval.
		p.probeDue(ctx, time.Now().Add(time.Hour))

		ticker := time.NewTicker(p.opts.Tick)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-p.stop:
				return
			case now := <-ticker.C:
				p.probeDue(ctx, now)
			}
		}
	}()
}

// probeDue probes every worker whose schedule has come up, concurrently: one slow worker
// must not delay the rest of the fleet's health.
func (p *Pool) probeDue(ctx context.Context, now time.Time) {
	var wg sync.WaitGroup
	for _, w := range p.workers {
		if w.dueAt(now).After(now) {
			continue
		}
		wg.Add(1)
		go func(w *worker) {
			defer wg.Done()

			before := w.snapshot()
			w.probe(ctx)
			p.logTransition(before, w.snapshot())
		}(w)
	}
	wg.Wait()
}

func (p *Pool) logTransition(before, after Snapshot) {
	switch {
	case !before.Ejected && after.Ejected:
		p.logger.Error().Str("worker", after.Addr).Int("failures", after.Failures).
			Msg("worker ejected")
	case before.Ejected && !after.Ejected:
		p.logger.Info().Str("worker", after.Addr).Msg("worker recovered")
	case !before.Ready && after.Ready:
		p.logger.Info().Str("worker", after.Addr).
			Str("model_version", after.ModelVersion).Msg("worker ready")
	case before.Ready && !after.Ready:
		p.logger.Warn().Str("worker", after.Addr).Int("failures", after.Failures).
			Msg("worker unhealthy")
	}
}

// Client is a worker checked out for one request.
type Client struct {
	Addr         string
	ModelVersion string
	Worker       workerpb.WorkerClient
}

// Pick returns the least-busy selectable worker.
//
// Least-busy is by free slots, with RTF breaking ties: between two equally idle workers,
// prefer the one that has been generating audio faster. Selection is advisory — the
// worker itself is the authority on its slot and answers RESOURCE_EXHAUSTED if it lost
// the race (ADR-003).
func (p *Pool) Pick() (Client, error) {
	var (
		best     *worker
		bestFree int32 = -1
		bestRTF        = float32(math.MaxFloat32)
	)

	for _, w := range p.workers {
		snap := w.snapshot()
		if !snap.Selectable() {
			continue
		}
		free := snap.Free()
		rtf := snap.RTFEWMA
		if rtf == 0 {
			// A worker that has served nothing yet has no RTF. Treat it as average so a
			// cold worker is neither starved nor preferred.
			rtf = 1
		}
		if free > bestFree || (free == bestFree && rtf < bestRTF) {
			best, bestFree, bestRTF = w, free, rtf
		}
	}

	if best == nil {
		return Client{}, ErrNoWorker
	}

	snap := best.snapshot()
	return Client{Addr: snap.Addr, ModelVersion: snap.ModelVersion, Worker: best.client}, nil
}

// Snapshots returns the current view of every worker, for /readyz and metrics.
func (p *Pool) Snapshots() []Snapshot {
	out := make([]Snapshot, 0, len(p.workers))
	for _, w := range p.workers {
		out = append(out, w.snapshot())
	}
	return out
}

// AdvertisedVoices reports every voice a ready worker can produce, mapped to the model
// version producing it. Implements voices.Fleet.
//
// A voice is listed as soon as one ready worker has it: during a rolling deploy the
// fleet is briefly mixed, and the cache key includes the model version, so a mixed fleet
// is safe (FL-06).
func (p *Pool) AdvertisedVoices() map[string]string {
	advertised := make(map[string]string)
	for _, snap := range p.Snapshots() {
		if !snap.Ready || snap.Ejected {
			continue
		}
		for _, id := range snap.VoiceIDs {
			advertised[id] = snap.ModelVersion
		}
	}
	return advertised
}

// ModelVersion returns the version a ready worker is serving, or "" when none is.
func (p *Pool) ModelVersion() string {
	for _, snap := range p.Snapshots() {
		if snap.Ready && !snap.Ejected {
			return snap.ModelVersion
		}
	}
	return ""
}

// Ready is the /readyz check: at least one worker must be ready (FL-06).
func (p *Pool) Ready(context.Context) error {
	for _, snap := range p.Snapshots() {
		if snap.Ready && !snap.Ejected {
			return nil
		}
	}
	return fmt.Errorf("%w: no ready worker", ErrNoWorker)
}

// Close stops polling and releases every connection. Safe to call more than once:
// shutdown paths often overlap, and a second call must not report a closed connection as
// a failure.
func (p *Pool) Close() error {
	p.stopOnce.Do(func() {
		close(p.stop)
		p.stopped.Wait()

		var errs []error
		for _, w := range p.workers {
			if err := w.close(); err != nil {
				errs = append(errs, err)
			}
		}
		p.closeErr = errors.Join(errs...)
	})
	return p.closeErr
}
