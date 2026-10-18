//go:build unix

package main

import (
	"context"
	"errors"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/blackwell-systems/go-agents-book/code/ch15/agent"
)

// TestShutdown checks that SIGTERM cancels the run's context with
// agent.ErrShutdown as the cause.
func TestShutdown(t *testing.T) {
	ctx, stop := withShutdown(t.Context())
	defer stop()
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ctx.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("SIGTERM didn't cancel the context")
	}
	if !errors.Is(context.Cause(ctx), agent.ErrShutdown) {
		t.Errorf("cause %v, want ErrShutdown", context.Cause(ctx))
	}
}
