//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package tracker

import (
	"fmt"
	"os"
	"syscall"
)

// lockFile takes an exclusive lock on the file at path, waiting for it,
// and returns the function that releases it.
func lockFile(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, fmt.Errorf("lock: %w", err)
	}
	return func() { f.Close() }, nil // closing releases the lock
}
