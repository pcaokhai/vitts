package dispatch

import "time"

// SetWaitForTest shortens the queue budgets so a test can reach a refusal without
// waiting the production two seconds. The decision under test is whether a caller is
// refused, not how long the budget is.
func (d *Dispatcher) SetWaitForTest(waitFor func(Class) time.Duration) { d.waitFor = waitFor }
