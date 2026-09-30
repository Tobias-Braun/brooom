package action

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// isSameEntry reports whether a and b name the same file system object. The
// comparison is by identity (volume and file index on Windows, device and
// inode elsewhere), so it is immune to spelling tricks such as 8.3 short
// names. Neither path is followed if it is a symlink. A path that does not
// exist cannot alias anything, but any other stat error (access denied on a
// protected entry, an I/O error) means identity is unknown, and unknown must
// fail closed: it reports "same" so the caller refuses. It is a variable so
// tests can simulate aliases a Linux file system cannot create.
var isSameEntry = func(a, b string) bool {
	fa, err := lstat(a)
	if err != nil {
		return !isAbsent(err)
	}
	fb, err := lstat(b)
	if err != nil {
		return !isAbsent(err)
	}
	return os.SameFile(fa, fb)
}

// lstat is os.Lstat, replaceable by tests that need a stat failure a
// privileged Linux test run cannot provoke with permissions.
var lstat = os.Lstat

// isAbsent reports whether a stat error means the entry is not there (also
// when a parent is a file), as opposed to an entry that exists but cannot be
// inspected.
func isAbsent(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR)
}

// ancestorsOf returns path and every parent directory of it up to, but not
// including, the volume root.
func ancestorsOf(path string) []string {
	var out []string
	for p := filepath.Clean(path); !isVolumeRoot(p); p = filepath.Dir(p) {
		out = append(out, p)
	}
	return out
}

// RefuseByIdentity is the spelling-independent half of the static trash
// refusals. The lexical checks of refuseTarget compare path strings, which an
// alias defeats: on Windows "C:\repo\GIT~1" is the repository's ".git" and
// "QUARAN~1" is Brooom's quarantine. This function compares file identity
// instead and refuses path when it, or any directory above it, is
//
//   - an entry that is the same object as the ".git" next to it,
//   - the same object as, or (for the path itself) a parent of, the Brooom
//     home or the user's home, or
//   - the same object as Brooom's sessions or quarantine directory.
//
// It runs in Plan, Apply and for `brooom clean --from` vetting, on the path
// after the guard resolved it. The returned error wraps ErrSkipped.
func RefuseByIdentity(path string) error {
	chain := ancestorsOf(path)
	for _, a := range chain {
		if isGitName(filepath.Base(a)) {
			continue // the lexical check already handles the real name
		}
		if isSameEntry(a, filepath.Join(filepath.Dir(a), ".git")) {
			return skipf("refusing to remove .git or anything inside it (%s is an alias of it)", filepath.Base(a))
		}
	}
	for p, why := range protectedPaths() {
		for _, q := range ancestorsOf(p) {
			if isSameEntry(path, q) {
				return skipf("refusing to remove %s or a directory containing it", why)
			}
		}
	}
	for _, s := range brooomStateDirs() {
		for _, a := range chain {
			if isSameEntry(a, s) {
				return skipf("refusing to remove Brooom's own session or quarantine data")
			}
		}
	}
	return nil
}
