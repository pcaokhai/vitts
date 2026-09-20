// Package degrade decides how long a dependency outage may be ignored.
//
// Rate limiting and quota both need Redis on every request. Failing closed makes Redis a
// single point of failure for an API with a 99.5% availability target (NFR-04); failing
// open without a bound turns one outage into unmetered, unlimited traffic. The runbook
// already specified the middle option — serve for a short window, then stop — and this is
// it (ADR-011, docs/13-runbook.md § Redis lost).
package degrade

import (
	"sync"
	"time"
)

// Window is how long a dependency may be unreachable before requests that need it are
// refused. Short enough that abuse is bounded, long enough to cover a restart.
const Window = 60 * time.Second

// Breaker tracks one dependency. The zero value is not usable; call New.
type Breaker struct {
	window time.Duration
	now    func() time.Time

	mu    sync.Mutex
	since time.Time // zero when the dependency is healthy
}

// New returns a breaker over the given window. now is time.Now outside tests.
func New(window time.Duration, now func() time.Time) *Breaker {
	if now == nil {
		now = time.Now
	}
	return &Breaker{window: window, now: now}
}

// Tolerate records a failure and reports whether the caller may proceed without the
// dependency. It returns false once the outage has lasted longer than the window.
func (b *Breaker) Tolerate() bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	now := b.now()
	if b.since.IsZero() {
		b.since = now
	}
	return now.Sub(b.since) <= b.window
}

// Succeed clears the outage. Calling it on every success is what makes the window
// measure a continuous outage rather than the time since the first-ever blip.
func (b *Breaker) Succeed() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.since = time.Time{}
}

// Degraded reports whether the dependency is currently failing. It is for readiness and
// metrics, and never decides a request on its own.
func (b *Breaker) Degraded() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return !b.since.IsZero()
}
