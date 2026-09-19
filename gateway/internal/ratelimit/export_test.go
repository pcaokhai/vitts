package ratelimit

import "time"

// SetClockForTest lets an integration test place a lease in the past, which is how an
// expired lease is exercised without sleeping for the TTL.
func (l *Limiter) SetClockForTest(now func() time.Time) { l.nowOverride = now }
