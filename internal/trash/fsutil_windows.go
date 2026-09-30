//go:build windows

package trash

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// crossDeviceErrno is ERROR_NOT_SAME_DEVICE (17), returned by MoveFileEx
// when source and destination are on different volumes. The literal avoids a
// golang.org/x/sys dependency for one constant.
const crossDeviceErrno = syscall.Errno(17)

// isCrossDevice reports whether err means a rename crossed volumes.
func isCrossDevice(err error) bool { return errors.Is(err, crossDeviceErrno) }

// clearReadOnly clears the read-only attribute on every non-link entry under
// root (os.Chmod with a write bit does this on Windows) and reports whether
// it changed anything worth retrying for. Links are skipped, never followed.
func clearReadOnly(root string) bool {
	changed := false
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		if info, ierr := d.Info(); ierr == nil && info.Mode().Perm()&0o200 == 0 {
			if os.Chmod(p, info.Mode().Perm()|0o200) == nil {
				changed = true
			}
		}
		return nil
	})
	return changed
}
