package agent

import (
	"cmp"
	"sync"
)

// tag::budget[]

// A RetryBudget limits retries across every run that shares it, the way
// gRPC throttles retries: it holds credits, each failed call worth
// retrying costs one, each success earns back Ratio, and retries stop
// while half or fewer are left. When a provider is failing for
// everyone, the shared budget runs dry and the agents stop piling
// retries onto it; a few successes refill it.
type RetryBudget struct {
	Max   float64 // credits when full; 0 means 10
	Ratio float64 // credits a success earns back; 0 means 0.1

	mu      sync.Mutex
	credits float64
	used    bool // credits holds a value; until then the budget is full
}

// Allow reports whether a retry may be sent now. A nil RetryBudget
// allows every retry.
func (b *RetryBudget) Allow() bool {
	if b == nil {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.level() > cmp.Or(b.Max, 10)/2
}

// end::budget[]

// tag::budget-record[]

// Record counts one call's outcome: ok for a success, !ok for a
// failure worth retrying. Permanent failures aren't recorded; they
// say nothing about whether the provider is healthy.
func (b *RetryBudget) Record(ok bool) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	t := b.level()
	if ok {
		t = min(t+cmp.Or(b.Ratio, 0.1), cmp.Or(b.Max, 10))
	} else {
		t = max(t-1, 0)
	}
	b.credits, b.used = t, true
}

// end::budget-record[]

// level returns the credits held; b.mu is held.
func (b *RetryBudget) level() float64 {
	if !b.used {
		return cmp.Or(b.Max, 10)
	}
	return b.credits
}
