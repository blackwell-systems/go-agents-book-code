package agent

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"time"
)

// tag::grace[]

// Grace returns a ToolWare that lets a call that may change something
// run on for up to d after the run is stopped with ErrShutdown, so a
// call already talking to the tracker can hear its answer. A finished
// call settles its outcome; a cancelled one leaves it unknown. Any
// other cancellation, such as the tool's own timeout, reaches the
// call at once (chapter 15).
func Grace(d time.Duration) ToolWare {
	return func(t Tool) Tool {
		if t.ReadOnly {
			return t
		}
		run := t.Run
		t.Run = func(ctx context.Context, args jsontext.Value) (string,
			error) {
			inner, stop := graced(ctx, d)
			defer stop()
			return run(inner, args)
		}
		return t
	}
}

// end::grace[]

// tag::graced[]

// graced returns a context that ctx's cancellation reaches d late if
// its cause is ErrShutdown, and at once otherwise. stop ends the
// goroutine that watches ctx.
func graced(ctx context.Context, d time.Duration) (context.Context,
	func()) {
	inner, cancel := context.WithCancelCause(
		context.WithoutCancel(ctx)) // <1>
	done := make(chan struct{})
	go func() {
		select {
		case <-done:
			return
		case <-ctx.Done():
		}
		cause := context.Cause(ctx)
		if errors.Is(cause, ErrShutdown) { // <2>
			select {
			case <-done:
				return
			case <-time.After(d):
			}
		}
		cancel(cause)
	}()
	return inner, func() { close(done); cancel(nil) }
}

// end::graced[]
