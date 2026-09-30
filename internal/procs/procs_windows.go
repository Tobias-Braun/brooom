//go:build windows

package procs

import (
	"context"
	"fmt"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// x/sys/windows has no Restart Manager wrappers, so the four calls are bound
// lazily. They return a Win32 error code directly (0 is success), not through
// GetLastError.
var (
	rstrtmgr                = windows.NewLazySystemDLL("rstrtmgr.dll")
	procRmStartSession      = rstrtmgr.NewProc("RmStartSession")
	procRmRegisterResources = rstrtmgr.NewProc("RmRegisterResources")
	procRmGetList           = rstrtmgr.NewProc("RmGetList")
	procRmEndSession        = rstrtmgr.NewProc("RmEndSession")
)

// cchRmSessionKey is CCH_RM_SESSION_KEY: the session key buffer holds 32
// characters plus the terminating NUL.
const cchRmSessionKey = 32

// openFiles tests files in batches and directories through their contents,
// see bisectLocked and lockedDirs for the strategy.
func openFiles(ctx context.Context, files, dirs []string, res map[string]bool) error {
	if err := probeRestartManager(); err != nil {
		return err
	}
	incomplete, err := checkBatches(ctx, files, lockedBy, res)
	if err != nil {
		return err
	}
	dirIncomplete, err := lockedDirs(ctx, dirs, lockedBy, res)
	if err != nil {
		return err
	}
	if incomplete || dirIncomplete {
		return fmt.Errorf("%w: some paths could not be checked (caps or unsupported paths)", ErrIncomplete)
	}
	return nil
}

// probeRestartManager makes sure the DLL and a session can be created at
// all, so a broken Restart Manager yields ErrUnavailable instead of every
// file being reported as unjudgeable.
func probeRestartManager() error {
	if err := rstrtmgr.Load(); err != nil {
		return fmt.Errorf("%w: loading rstrtmgr.dll: %w", ErrUnavailable, err)
	}
	h, err := rmStart()
	if err != nil {
		return err
	}
	rmEnd(h)
	return nil
}

// rmStart opens a Restart Manager session. Every call must be paired with
// rmEnd because a user may only hold 64 sessions at a time.
func rmStart() (uint32, error) {
	var handle uint32
	var key [cchRmSessionKey + 1]uint16
	r, _, _ := procRmStartSession.Call(
		uintptr(unsafe.Pointer(&handle)),
		0,
		uintptr(unsafe.Pointer(&key[0])),
	)
	if r != 0 {
		return 0, fmt.Errorf("%w: RmStartSession: %w", ErrUnavailable, syscall.Errno(r))
	}
	return handle, nil
}

// rmEnd closes a session; there is nothing useful to do if that fails.
func rmEnd(handle uint32) {
	_, _, _ = procRmEndSession.Call(uintptr(handle))
}

// lockedBy registers all files in one fresh session and asks whether any
// process uses one of them. Restart Manager cannot unregister resources, so
// each query gets its own session, always ended before the next one starts.
// RmGetList is called with an empty buffer: ERROR_MORE_DATA or a non-zero
// needed count already answers the yes/no question without fetching details.
func lockedBy(files []string) (bool, error) {
	if len(files) == 0 {
		return false, nil
	}
	ptrs := make([]*uint16, len(files))
	for i, f := range files {
		p, err := windows.UTF16PtrFromString(extendedPath(f))
		if err != nil {
			return false, fmt.Errorf("procs: path %q: %w", f, err)
		}
		ptrs[i] = p
	}
	handle, err := rmStart()
	if err != nil {
		return false, err
	}
	defer rmEnd(handle)

	r, _, _ := procRmRegisterResources.Call(
		uintptr(handle),
		uintptr(len(ptrs)),
		uintptr(unsafe.Pointer(&ptrs[0])),
		0, 0, 0, 0,
	)
	runtime.KeepAlive(ptrs)
	if r != 0 {
		return false, fmt.Errorf("procs: RmRegisterResources: %w", syscall.Errno(r))
	}
	return rmHasProcesses(handle)
}

// rmHasProcesses queries the affected process count of a session.
func rmHasProcesses(handle uint32) (bool, error) {
	var needed, have, reasons uint32
	r, _, _ := procRmGetList.Call(
		uintptr(handle),
		uintptr(unsafe.Pointer(&needed)),
		uintptr(unsafe.Pointer(&have)),
		0,
		uintptr(unsafe.Pointer(&reasons)),
	)
	switch {
	case syscall.Errno(r) == windows.ERROR_MORE_DATA:
		return true, nil
	case r != 0:
		return false, fmt.Errorf("procs: RmGetList: %w", syscall.Errno(r))
	}
	return needed > 0, nil
}
