package agent

import (
	"context"
	"iter"
	"sync"
)

// tag::merge-start[]

// Merge runs several event streams at once, such as one agent's run per
// fingerprint group, and yields their events as they happen, each tagged with
// the name of the run it came from. Each run's StoppedEvent comes
// through like any other event, even when ctx is cancelled; Merge ends
// when every run has stopped. Breaking out of the range loop cancels
// every run and waits for their goroutines to finish, so none is left
// blocked on a send.
func Merge(ctx context.Context,
	runs map[string]func(context.Context) iter.Seq[Event],
) iter.Seq[Event] {
	return func(yield func(Event) bool) {
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		events := make(chan Event)
		// closed when the caller stops reading
		quit := make(chan struct{})
		var wg sync.WaitGroup
		for name, run := range runs {
			wg.Go(func() {
				for ev := range run(ctx) { // <1>
					ev.Agent = name
					select {
					case events <- ev:
					case <-quit: // <2>
						return
					}
				}
			})
		}
		go func() { wg.Wait(); close(events) }() // <3>
		// end::merge-start[]
		// tag::merge-read[]
		for ev := range events {
			if !yield(ev) {
				close(quit)
				cancel()
				for range events { // <1>
				}
				return
			}
		}
	}
}

// end::merge-read[]
