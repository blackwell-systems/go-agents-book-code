package agent

import (
	"fmt"
	"maps"
	"slices"
)

// FS is a small in-memory file system: file names and their contents.
// The tools in this chapter work on an FS, so nothing touches your disk.
// It is not safe for concurrent use; the loop runs one tool at a time.
type FS map[string]string

// List returns the file names in sorted order.
func (fs FS) List() []string {
	return slices.Sorted(maps.Keys(fs))
}

// Read returns a file's contents.
func (fs FS) Read(name string) (string, error) {
	s, ok := fs[name]
	if !ok {
		return "", fmt.Errorf("no file named %q", name)
	}
	return s, nil
}
