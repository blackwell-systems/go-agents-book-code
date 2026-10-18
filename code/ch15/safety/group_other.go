//go:build !unix

package safety

import "os/exec"

// On Windows a process group needs a Job Object, which the book doesn't
// cover; the go command alone is killed on timeout.
func group(*exec.Cmd)     {}
func killGroup(*exec.Cmd) {}
