//go:build windows

package scope

import (
	"path/filepath"

	"golang.org/x/sys/windows"
)

// canonicalLast returns p with its final element spelled as the file system
// stores it: an 8.3 alias such as GIT~1 becomes ".git", and the letter case
// is the real one. GetLongPathName never follows a reparse point, so a link
// keeps naming the link. Failure (the entry does not exist, path too long)
// keeps the input; the caller's later checks then see the given spelling.
func canonicalLast(p string) string {
	in, err := windows.UTF16PtrFromString(p)
	if err != nil {
		return p
	}
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n, err := windows.GetLongPathName(in, &buf[0], uint32(len(buf)))
	if err != nil || n == 0 || int(n) > len(buf) {
		return p
	}
	long := windows.UTF16ToString(buf[:n])
	// Only the final element is adopted: the parent was resolved already
	// and its spelling is what the guard compared.
	return filepath.Join(filepath.Dir(p), filepath.Base(long))
}

// maxPathLen is the extended-length path limit of Windows.
const maxPathLen = 32767

func checkInput(p string) error { return winCheckInput(p) }

func normalizeExtended(p string) string { return winNormalizeExtended(p) }

func rejectComponent(c string) error { return winRejectComponent(c) }

// normalizeExisting lets Windows report the canonical spelling of the
// existing prefix: long names instead of 8.3 short names (PROGRA~1), the real
// letter case, and junction targets. Failure keeps the input, which is
// already symlink free.
func normalizeExisting(p string) string {
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}
	return p
}
