package action

import (
	"errors"
	"io/fs"
	"path/filepath"
	"syscall"

	"github.com/Tobias-Braun/brooom/internal/walk"
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
	ia, err := identityOf(a, false)
	if err != nil {
		return !isAbsent(err)
	}
	ib, err := identityOf(b, false)
	if err != nil {
		return !isAbsent(err)
	}
	return ia.sameAs(ib)
}

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

// refuseVCSAlias refuses a when it is the same object as a VCS metadata entry
// (".git", ".hg", ".jj", ".svn") next to it. When the comparison only reports
// "same" because an entry could not be inspected (isSameEntry fails closed),
// the message names that cause instead of claiming an alias.
func refuseVCSAlias(a string) error {
	dir := filepath.Dir(a)
	for _, name := range walk.VCSNames() {
		sibling := filepath.Join(dir, name)
		if !isSameEntry(a, sibling) {
			continue
		}
		if err := inspectError(a, sibling); err != nil {
			return skipf("refusing to remove %s: cannot tell whether it is an alias of %s: %v", a, name, err)
		}
		return skipf("refusing to remove %s or anything inside it (%s is an alias of it)", name, filepath.Base(a))
	}
	return nil
}

// inspectError returns the first failure other than "not there" reading the
// identity of the paths, or nil when all could be read or are absent.
func inspectError(paths ...string) error {
	for _, p := range paths {
		if _, err := identityOf(p, false); err != nil && !isAbsent(err) {
			return err
		}
	}
	return nil
}

// RefuseByIdentity is the spelling-independent half of the static trash
// refusals. The lexical checks of refuseTarget compare path strings, which an
// alias defeats: on Windows "C:\repo\GIT~1" is the repository's ".git" and
// "SESSIO~1" is Brooom's sessions directory. This function compares file identity
// instead and refuses path when it, or any directory above it, is
//
//   - an entry that is the same object as the ".git" next to it,
//   - the same object as, or (for the path itself) a parent of, the Brooom
//     home or the user's home, or
//   - the same object as Brooom's sessions directory.
//
// It runs in Plan and Apply, on the path after the guard resolved it. The returned error wraps ErrSkipped.
func RefuseByIdentity(path string) error {
	chain := ancestorsOf(path)
	for _, a := range chain {
		if isGitName(filepath.Base(a)) {
			continue // the lexical check already handles the real name
		}
		if err := refuseVCSAlias(a); err != nil {
			return err
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
				return skipf("refusing to remove Brooom's own session data")
			}
		}
	}
	return nil
}
