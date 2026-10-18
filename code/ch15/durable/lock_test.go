//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package durable

import (
	"errors"
	"os/exec"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestReopenWhileStarting reopens a run right after closing it while
// other goroutines start programs, as the agent's tools start go test.
// A child holds the lock file from fork until exec, so a Close that
// only closed the file left the run locked for that moment.
func TestReopenWhileStarting(t *testing.T) {
	prog, err := exec.LookPath("true")
	if err != nil {
		t.Skip("no true command to start")
	}
	dir, ctx := t.TempDir(), t.Context()
	var stop atomic.Bool
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for !stop.Load() {
				_ = exec.CommandContext(ctx, prog).Run() // only starting matters
			}
		})
	}
	defer wg.Wait()
	defer stop.Store(true)
	locked := 0
	deadline := time.Now().Add(2 * time.Second)
	for i := 0; i < 2000 && time.Now().Before(deadline); i++ {
		c, err := Open(dir, "run-1")
		if errors.Is(err, ErrLocked) {
			locked++
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if err := c.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if locked > 0 {
		t.Errorf("%d reopens found the run locked by this process", locked)
	}
}
