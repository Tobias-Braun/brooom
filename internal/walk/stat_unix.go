//go:build !windows

package walk

import (
	"io/fs"
	"strconv"
	"syscall"
)

// fileID identifies a file for hard link detection.
type fileID struct {
	dev, ino, nlink uint64
	ok              bool
}

// u64 converts the platform-specific integer widths of Stat_t fields (they
// differ between Linux and macOS) without tripping over redundant casts.
func u64[T ~int32 | ~uint16 | ~uint32 | ~uint64 | ~int64](v T) uint64 { return uint64(v) }

// allocatedSize returns the bytes allocated on disk, straight from the
// Stat_t that the directory read already produced (no extra syscall). Blocks
// are always counted in 512 byte units, independent of the filesystem block
// size.
func allocatedSize(fi fs.FileInfo) int64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return st.Blocks * 512
	}
	return fi.Size()
}

// fileIDOf extracts device, inode and link count.
func fileIDOf(fi fs.FileInfo) fileID {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return fileID{}
	}
	return fileID{dev: u64(st.Dev), ino: u64(st.Ino), nlink: u64(st.Nlink), ok: true}
}

// String is the dev:ino form stored in cache records.
func (f fileID) String() string {
	if !f.ok {
		return ""
	}
	return strconv.FormatUint(f.dev, 16) + ":" + strconv.FormatUint(f.ino, 16)
}
