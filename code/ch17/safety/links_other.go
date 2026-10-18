//go:build !unix

package safety

import "io/fs"

// links can't count a file's names here; Windows needs a file handle
// for that, which the book doesn't cover.
func links(fs.FileInfo) uint64 { return 1 }
