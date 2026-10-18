//go:build !(darwin || dragonfly || freebsd || linux || netbsd || openbsd)

package tracker

// lockFile would lock the file at path. Elsewhere than on systems with
// flock, a store relies on being the only one using its file.
func lockFile(string) (func(), error) { return func() {}, nil }
