package main

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/blackwell-systems/go-agents-book/code/ch12/replay"
	"github.com/blackwell-systems/go-agents-book/code/ch16/task"
)

// cancelled runs each version on the retry script, cancels the run
// after the given delay, while a retry may be waiting, and prints how
// long each took to return. The times vary from run to run, so they
// are printed, not checked.
func cancelled(ctx context.Context, out io.Writer,
	after time.Duration) error {
	for _, v := range versions {
		c, _ := task.Replay()
		ctx, cancel := context.WithCancel(replay.WithScript(ctx, "retry"))
		stop := time.AfterFunc(after, cancel)
		start := time.Now()
		_, err := v.Run(ctx, c, &task.Env{})
		took := time.Since(start)
		stop.Stop()
		cancel()
		if _, werr := fmt.Fprintf(out, "%s: returned after %v (%v)\n",
			v.Name, took.Round(time.Millisecond), err); werr != nil {
			return fmt.Errorf("print: %w", werr)
		}
	}
	return nil
}
