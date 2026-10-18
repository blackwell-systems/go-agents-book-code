//go:build unix && !darwin

package safety

import "errors"

// readSysctl is used only on macOS, where the profile filters sysctls.
func readSysctl(string) error { return errors.ErrUnsupported }
