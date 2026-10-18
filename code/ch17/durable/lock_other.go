//go:build !(darwin || dragonfly || freebsd || linux || netbsd || openbsd)

package durable

import (
	"errors"
	"fmt"
	"os"
	"runtime"
)

// lock would take an exclusive lock on f. This package locks only on
// systems with flock; elsewhere, opening a run fails rather than let
// two processes run it at once.
func lock(*os.File) error {
	return fmt.Errorf("lock: %w on %s", errors.ErrUnsupported,
		runtime.GOOS)
}

// unlock has no lock to release where lock always fails.
func unlock(*os.File) error { return nil }
