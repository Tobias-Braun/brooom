//go:build windows

package walk

import "io/fs"

// fileID is empty on Windows: hard links are not deduplicated and directory
// records carry no identity.
type fileID struct {
	dev, ino, nlink uint64
	ok              bool
}

// allocatedSize is the logical size on Windows because the allocated size is
// not cheaply available from the directory listing.
func allocatedSize(fi fs.FileInfo) int64 { return fi.Size() }

// fileIDOf reports no identity.
func fileIDOf(fs.FileInfo) fileID { return fileID{} }

// String is always empty on Windows.
func (fileID) String() string { return "" }
