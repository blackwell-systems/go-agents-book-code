//go:build unix

package workspace

import (
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// TestAtNoHardlinks: the workspace's objects are copies, so changing
// one can't change the source repository's.
func TestAtNoHardlinks(t *testing.T) {
	src, old := repo(t)
	dst := filepath.Join(t.TempDir(), "ws")
	if err := At(t.Context(), src, old, dst); err != nil {
		t.Fatal(err)
	}
	n := 0
	err := filepath.WalkDir(filepath.Join(dst, ".git", "objects"),
		func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			fi, err := os.Stat(p)
			if err != nil {
				return err //nolint:wrapcheck // a test helper
			}
			if st, ok := fi.Sys().(*syscall.Stat_t); ok && st.Nlink > 1 {
				n++
			}
			return nil
		})
	if err != nil || n > 0 {
		t.Errorf("%d objects hard-linked, %v", n, err)
	}
}
