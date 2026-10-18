//go:build !unix

package main

import "errors"

// killSelf would end this process with SIGKILL, for -die-after; this
// system has no such signal.
func killSelf() error {
	return errors.New("no SIGKILL on this system")
}
