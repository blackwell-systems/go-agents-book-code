//go:build unix

package safety

import (
	"io/fs"
	"syscall"
)

// links returns how many names a file has: more than one means a hard
// link somewhere else in the file system reaches the same file.
func links(info fs.FileInfo) uint64 {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Nlink)
	}
	return 1
}
