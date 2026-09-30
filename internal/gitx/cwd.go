package gitx

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// PathWithin reports whether p is dir itself or lies below it. Like SamePath
// the check is lexical and case-insensitive where the filesystem usually is,
// so it also works for paths that do not exist.
func PathWithin(dir, p string) bool {
	return pathWithinOS(runtime.GOOS, dir, p)
}

func pathWithinOS(goos, dir, p string) bool {
	if samePathOS(goos, dir, p) {
		return true
	}
	dir, p = cleanNative(dir), cleanNative(p)
	prefix := dir
	if !strings.HasSuffix(prefix, string(filepath.Separator)) {
		prefix += string(filepath.Separator)
	}
	if len(p) < len(prefix) {
		return false
	}
	return samePathOS(goos, prefix, p[:len(prefix)])
}

// CwdWithin reports whether the working directory of this very process is dir
// or below it. Removing such a worktree would leave the caller's shell in a
// vanished directory, and no per-process scan (procs) is needed to know it:
// this holds on every OS, also where open-file detection is unavailable. The
// working directory is compared both as reported and symlink-resolved, since
// dir usually comes from scope.Guard and is resolved. An unknown working
// directory is not inside anything.
func CwdWithin(dir string) bool {
	cwd, err := os.Getwd()
	if err != nil {
		return false
	}
	if PathWithin(dir, cwd) {
		return true
	}
	resolved, err := filepath.EvalSymlinks(cwd)
	return err == nil && PathWithin(dir, resolved)
}
