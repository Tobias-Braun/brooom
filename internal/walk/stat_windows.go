//go:build windows

package walk

import (
	"io/fs"

	"golang.org/x/sys/windows"
)

// reparseTagNameSurrogate is bit 29 of a reparse tag; symlinks and junctions
// carry it, cloud placeholders and ProjFS do not.
const reparseTagNameSurrogate = 0x20000000

// nameSurrogate reports whether the reparse point at path is a name
// surrogate (symlink, junction), by the tag FindFirstFile returns. When the
// tag cannot be read the entry counts as a redirect, so it is not descended:
// unknown means unsafe. It is a variable so tests can simulate reparse points.
var nameSurrogate = func(path string) bool {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return true
	}
	var data windows.Win32finddata
	h, err := windows.FindFirstFile(p, &data)
	if err != nil {
		return true
	}
	_ = windows.FindClose(h)
	if data.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT == 0 {
		return false
	}
	return data.Reserved0&reparseTagNameSurrogate != 0
}

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
