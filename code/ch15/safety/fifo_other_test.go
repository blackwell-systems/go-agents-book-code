//go:build !unix

package safety

import "errors"

// mkfifo would make a named pipe; this system has none.
func mkfifo(string) error { return errors.ErrUnsupported }
