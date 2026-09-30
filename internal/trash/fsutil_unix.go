//go:build !windows

package trash

import (
	"errors"
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
