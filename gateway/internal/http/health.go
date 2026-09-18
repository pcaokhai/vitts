package http

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"sync"
	"time"
)

// checkTimeout bounds a single readiness probe. A dependency that is merely slow must not
// hold the endpoint open; /readyz is polled by orchestrators on a short interval.
const checkTimeout = 2 * time.Second

// Check reports whether one dependency is usable. Adapters register their own:
// Postgres in task 1.2, Redis in 1.4, the worker pool in 1.6.
type Check func(context.Context) error

// Readiness is the set of checks /readyz reports. Empty means ready: a gateway with no
// dependencies wired yet is genuinely ready to serve what it has.
type Readiness struct {
	mu     sync.RWMutex
	checks map[string]Check
}

// NewReadiness returns an empty registry.
func NewReadiness() *Readiness {
	return &Readiness{checks: make(map[string]Check)}
}

// Register adds a named check, replacing any check with the same name.
func (r *Readiness) Register(name string, check Check) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.checks[name] = check
}

// Status is the /readyz body: overall verdict plus one entry per dependency.
type Status struct {
	Ready   bool              `json:"ready"`
	Checks  map[string]string `json:"checks"`
	Failing []string          `json:"failing,omitempty"`
}

// Evaluate runs every check and reports the result. Checks run concurrently: readiness
// latency should be the slowest dependency, not their sum.
func (r *Readiness) Evaluate(ctx context.Context) Status {
	r.mu.RLock()
	checks := make(map[string]Check, len(r.checks))
	for name, check := range r.checks {
		checks[name] = check
	}
	r.mu.RUnlock()

	status := Status{Ready: true, Checks: make(map[string]string, len(checks))}

	var (
		mu sync.Mutex
		wg sync.WaitGroup
	)
	for name, check := range checks {
		wg.Add(1)
		go func(name string, check Check) {
			defer wg.Done()

			ctx, cancel := context.WithTimeout(ctx, checkTimeout)
			defer cancel()
			err := check(ctx)

			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				status.Ready = false
				status.Checks[name] = "failing"
				status.Failing = append(status.Failing, name)
				return
			}
			status.Checks[name] = "ok"
		}(name, check)
	}
	wg.Wait()

	sort.Strings(status.Failing)
	return status
}

// Healthz answers liveness: the process is running and can serve. It must not touch a
// dependency, or a database blip would restart healthy gateways.
func Healthz() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}
}

// Readyz answers readiness: every registered dependency is usable.
func Readyz(readiness *Readiness) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		status := readiness.Evaluate(r.Context())

		code := http.StatusOK
		if !status.Ready {
			code = http.StatusServiceUnavailable
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		if err := json.NewEncoder(w).Encode(status); err != nil {
			return // response is already committed; the access log records the status
		}
	}
}
