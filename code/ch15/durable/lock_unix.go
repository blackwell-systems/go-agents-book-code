//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package durable

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// tag::lock[]

// lock takes an exclusive lock on f without waiting. The lock belongs
// to f's open file description, so a second Open in the same process
// is refused too. The kernel drops it when the process exits, however
// it exits, so a crash never leaves a run locked.
func lock(f *os.File) error {
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return ErrLocked
	}
	if err != nil {
		return fmt.Errorf("lock: %w", err)
	}
	return nil
}

// unlock releases the lock, for Close to call before it closes f. A
// program another goroutine is starting shares f's open file
// description from fork until exec, so closing f alone can leave the
// lock held for that moment.
func unlock(f *os.File) error {
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_UN); err != nil {
		return fmt.Errorf("unlock: %w", err)
	}
	return nil
}

// end::lock[]
