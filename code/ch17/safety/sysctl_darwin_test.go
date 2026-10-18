package safety

import "syscall"

// readSysctl reads the kernel value name, for the helper's sysctl mode.
func readSysctl(name string) error {
	_, err := syscall.Sysctl(name)
	return err //nolint:wrapcheck // the helper prints only whether it failed
}
