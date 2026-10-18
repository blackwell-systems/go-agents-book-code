package agent

import (
	"fmt"
	"sync"
)

// tag::spend[]

// A SpendPool is a cost budget shared by runs going on at once, such as
// one agent per fingerprint group: a run checks the pool before each model
// call and adds what the call cost after it. A quick run leaves its
// money for the others, which a budget split evenly in advance can't.
//
// The check and the spending are separate steps, so the pool can end
// above its limit: each of N runs can pass the check and then make one
// more call. Keep the limit that much below what you can afford.
type SpendPool struct {
	Limit float64 // dollars; 0 means no limit

	// Parent, if set, is a larger pool this one's spending also counts
	// against, such as a whole batch's over each group's run: a call is
	// refused when either pool has reached its limit (chapter 17).
	Parent *SpendPool

	mu    sync.Mutex
	spent float64
}

// end::spend[]

// Add records the cost of a call. A nil pool records nothing.
func (p *SpendPool) Add(cost float64) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.spent += cost
	p.mu.Unlock()
	p.Parent.Add(cost)
}

// Spent returns what all the runs have spent so far; a nil pool has
// spent nothing.
func (p *SpendPool) Spent() float64 {
	if p == nil {
		return 0
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.spent
}

// tag::spend-check[]

// check returns ErrBudgetExceeded once the pool has reached its limit.
func (p *SpendPool) check() error {
	if p == nil {
		return nil
	}
	if spent := p.Spent(); p.Limit > 0 && spent >= p.Limit {
		return fmt.Errorf("%w: the shared pool spent $%.6f of $%.6f",
			ErrBudgetExceeded, spent, p.Limit)
	}
	return p.Parent.check() // chapter 17
}

// end::spend-check[]
