package action

import (
	"os"
	"path/filepath"
)

// isSameEntry reports whether a and b name the same file system object. The
// comparison is by identity (volume and file index on Windows, device and
// inode elsewhere), so it is immune to spelling tricks such as 8.3 short
// names. Neither path is followed if it is a symlink. Any stat error means
// "not the same": a path that does not exist cannot alias anything. It is a
// variable so tests can simulate aliases a Linux file system cannot create.
var isSameEntry = func(a, b string) bool {
	fa, err := os.Lstat(a)
	if err != nil {
		return false
	}
	fb, err := os.Lstat(b)
	if err != nil {
		return false
	}
	return os.SameFile(fa, fb)
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
