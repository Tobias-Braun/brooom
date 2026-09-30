//go:build windows

package trash

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/windows"

	"github.com/Tobias-Braun/brooom/internal/procs"
)

// crossDeviceErrno is ERROR_NOT_SAME_DEVICE (17), returned by MoveFileEx
// when source and destination are on different volumes. The literal avoids a
// golang.org/x/sys dependency for one constant.
const crossDeviceErrno = syscall.Errno(17)

// isCrossDevice reports whether err means a rename crossed volumes.
func isCrossDevice(err error) bool { return errors.Is(err, crossDeviceErrno) }

// defaultProbeInUse asks the Restart Manager wrapper in package procs whether
// a process has a file below src open. A cross-device move copies first and
// deletes afterwards; a locked file would make that deletion fail part-way
// and leave a half-removed source that blocks undo, so it is refused before
// anything is copied.
func defaultProbeInUse(ctx context.Context, src string) error {
	return checkNotInUse(ctx, src, procs.OpenFiles)
}

// isLockedError reports the Win32 errors a file held open by another process
// produces: ERROR_ACCESS_DENIED, ERROR_SHARING_VIOLATION and
// ERROR_LOCK_VIOLATION. Access denied is included on purpose, it is what
// Windows returns for deleting an open file.
func isLockedError(err error) bool {
	return errors.Is(err, syscall.Errno(5)) || errors.Is(err, syscall.Errno(errorSharingViolation)) || errors.Is(err, syscall.Errno(errorLockViolation))
}

// describeIrregular names an entry Go reports as irregular. Since Go 1.23 a
// junction (mount point) is only ModeIrregular, so it is told apart from
// other special files by its reparse attribute.
func describeIrregular(p string, t fs.FileMode) string {
	if link, err := isReparsePoint(p); err == nil && link {
		return "a junction or other reparse point"
	}
	return fmt.Sprintf("a special file (%s)", t.Type())
}

// symlinkFlagUnprivileged lets a process create symlinks without the
// SeCreateSymbolicLink privilege when Developer Mode is on.
const symlinkFlagUnprivileged = 0x2

// createSymlink recreates the link src at dst with the given target string.
// os.Symlink picks the directory flag by statting the target relative to dst,
// which does not exist yet while its tree is still being copied, so a
// relative directory link would become a file link. The flag is decided from
// the original link instead (following it in the source tree, read-only), and
// a missing symlink privilege gets an actionable message.
func createSymlink(src, target, dst string) error {
	var flags uint32 = symlinkFlagUnprivileged
	if fi, err := os.Stat(src); err == nil && fi.IsDir() {
		flags |= windows.SYMBOLIC_LINK_FLAG_DIRECTORY
	}
	link, err := windows.UTF16PtrFromString(dst)
	if err != nil {
		return err
	}
	tgt, err := windows.UTF16PtrFromString(filepath.FromSlash(target))
	if err != nil {
		return err
	}
	err = windows.CreateSymbolicLink(link, tgt, flags)
	if errors.Is(err, windows.ERROR_PRIVILEGE_NOT_HELD) {
		return fmt.Errorf("cannot create symlink %q: Windows requires the symlink privilege (enable Developer Mode or run as administrator): %w", dst, err)
	}
	if err != nil {
		return &os.LinkError{Op: "symlink", Old: target, New: dst, Err: err}
	}
	return nil
}

// sameLinkKind checks that the copy of a link has the same file/directory
// flag as the original, which a target-string comparison cannot see.
func sameLinkKind(src, dst string) error {
	sa, err := linkAttrs(src)
	if err != nil {
		return err
	}
	da, err := linkAttrs(dst)
	if err != nil {
		return err
	}
	if sa&windows.FILE_ATTRIBUTE_DIRECTORY != da&windows.FILE_ATTRIBUTE_DIRECTORY {
		return fmt.Errorf("link %q and its copy %q differ in kind (directory link versus file link)", src, dst)
	}
	return nil
}

// linkAttrs returns the file attributes of the link itself.
func linkAttrs(p string) (uint32, error) {
	ptr, err := windows.UTF16PtrFromString(p)
	if err != nil {
		return 0, err
	}
	return windows.GetFileAttributes(ptr)
}

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
