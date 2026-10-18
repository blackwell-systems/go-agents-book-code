package agent

import (
	"context"
	"fmt"
)

// tag::slots[]

// Slots limits how many things run at once, such as the tool calls of
// one reply, or test runs across every agent in a program. Make one with
// NewSlots; a nil Slots would wait forever.
type Slots chan struct{}

// NewSlots returns Slots that let n things run at once.
func NewSlots(n int) Slots { return make(Slots, max(n, 1)) }

// Acquire waits for a free slot, and gives up when ctx is done,
// returning why. Every successful Acquire needs a Release.
func (s Slots) Acquire(ctx context.Context) error {
	select {
	case s <- struct{}{}:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("wait for a slot: %w", context.Cause(ctx))
	}
}

// Release frees a slot that Acquire took.
func (s Slots) Release() { <-s }

// end::slots[]
