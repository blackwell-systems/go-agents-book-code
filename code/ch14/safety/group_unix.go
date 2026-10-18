//go:build unix

package safety

import (
	"os/exec"
	"syscall"
)

// tag::group[]

// group starts the command in a process group of its own, and makes
// cancelling it kill the whole group: go test starts a test binary, and
// the test can start more, none of which a kill of the go command alone
// would reach. A child that starts its own session leaves the group and
// escapes this; only a rung-3 sandbox (bubblewrap's --die-with-parent,
// a container) catches that.
func group(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}

// killGroup kills whatever is left in the group after the go command
// exits, such as a test's child that was still running.
func killGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		// ESRCH, the only error, means none was left.
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}

// end::group[]
