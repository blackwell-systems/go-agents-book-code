//go:build unix

package main

import (
	"fmt"
	"os"
	"syscall"
)

// killSelf ends this process with SIGKILL, for -die-after: no deferred
// call runs and nothing is saved, as in a crash.
func killSelf() error {
	if err := syscall.Kill(os.Getpid(), syscall.SIGKILL); err != nil {
		return fmt.Errorf("kill: %w", err)
	}
	return nil
}
