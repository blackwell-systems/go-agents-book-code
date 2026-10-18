package agent

import (
	"fmt"
	"maps"
	"os"
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

// tag::write-dir[]

// WriteDir writes the files into dir. It opens dir as an os.Root, so a
// file name such as "../x" can't write outside it.
func (fs FS) WriteDir(dir string) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return fmt.Errorf("write files: %w", err)
	}
	defer root.Close()
	for _, name := range fs.List() {
		err := root.WriteFile(name, []byte(fs[name]), 0o644)
		if err != nil {
			return fmt.Errorf("write files: %w", err)
		}
	}
	return nil
}

// end::write-dir[]
