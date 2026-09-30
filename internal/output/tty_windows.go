//go:build windows

package output

import (
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

// IsMSYSPty reports whether f is the pipe behind an MSYS/Cygwin pseudo
// terminal (mintty, Git Bash). Only pipes are asked for their name, so a
// file or console handle never reaches the name query.
func IsMSYSPty(f *os.File) bool {
	h := windows.Handle(f.Fd())
	if t, err := windows.GetFileType(h); err != nil || t != windows.FILE_TYPE_PIPE {
		return false
	}
	// FILE_NAME_INFO is a uint32 length followed by UTF-16 units; the buffer
	// holds the longest name a pipe can have with room to spare.
	buf := make([]byte, 4+2*windows.MAX_PATH)
	if err := windows.GetFileInformationByHandleEx(h, windows.FileNameInfo, &buf[0], uint32(len(buf))); err != nil {
		return false
	}
	n := *(*uint32)(unsafe.Pointer(&buf[0])) / 2
	if int(n) > (len(buf)-4)/2 {
		return false
	}
	units := unsafe.Slice((*uint16)(unsafe.Pointer(&buf[4])), n)
	return isMSYSPtyName(windows.UTF16ToString(units))
}
