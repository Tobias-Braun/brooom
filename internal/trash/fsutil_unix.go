//go:build !windows

package trash

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"syscall"
)

// crossDeviceErrno is the rename error for "source and destination are on
// different filesystems".
const crossDeviceErrno = syscall.EXDEV

// isCrossDevice reports whether err means a rename crossed filesystems.
func isCrossDevice(err error) bool { return errors.Is(err, crossDeviceErrno) }

// clearReadOnly is a no-op on unix: permissions there are not cleared
// implicitly and removal failures are real. It reports that nothing changed.
func clearReadOnly(string) bool { return false }

// defaultProbeInUse does nothing on unix: an open file does not stop a copy
// or the removal of its directory entry there, so there is nothing to probe.
func defaultProbeInUse(context.Context, string) error { return nil }

// isLockedError is always false on unix, which has no mandatory file locks.
func isLockedError(error) bool { return false }

// describeIrregular names a directory entry that is neither file, directory
// nor symlink.
func describeIrregular(_ string, t fs.FileMode) string {
	return fmt.Sprintf("a special file (%s)", t.Type())
}

// createSymlink recreates the link src (whose target string is target) at
// dst. Unix has one kind of symlink, so the plain call is enough.
func createSymlink(_, target, dst string) error { return os.Symlink(target, dst) }

// sameLinkKind is trivially true on unix: links carry no file/directory flag.
func sameLinkKind(_, _ string) error { return nil }
