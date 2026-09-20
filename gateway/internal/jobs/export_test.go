package jobs

import (
	"context"
	"time"
)

// The orchestrator's steps are unexported because nothing outside this package should
// drive them. Tests need to, so they can replay one step at a time the way a crash and
// a stream replay would, rather than racing the consumer loop.

// HandleJobForTest runs the job step.
func (o *Orchestrator) HandleJobForTest(ctx context.Context, entry QueueEntry) error {
	return o.handleJob(ctx, entry)
}

// HandleSegmentForTest runs one segment step.
func (o *Orchestrator) HandleSegmentForTest(ctx context.Context, entry QueueEntry) error {
	return o.handleSegment(ctx, entry)
}

// SetSleepForTest removes retry backoff, so a test exercises the retry policy rather
// than the clock.
func (o *Orchestrator) SetSleepForTest(sleep func(context.Context, time.Duration) error) {
	o.sleep = sleep
}
