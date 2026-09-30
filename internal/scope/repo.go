package scope

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// gitFileProbeSize bounds how much of a ".git" file is read to recognize a
// "gitdir:" pointer, so a huge file named .git cannot stall detection.
const gitFileProbeSize = 4096

// FindRepoRoot walks up from start to the nearest directory containing a
// .git entry and returns its absolute, symlink-resolved path, or an error
// wrapping ErrNotInRepo.
//
// A .git entry is a directory (or a symlink to one), or a regular file whose
// content starts with "gitdir:" (linked worktrees and submodules). Any other
// file named .git is ignored and the walk continues. The nearest repository
// wins, so a submodule inside a repository returns the submodule. If start is
// a file its directory is used. The start is symlink-resolved first, so a
// symlinked working directory yields the real repository path.
//
// A start that does not exist returns the underlying file system error, not
// ErrNotInRepo. Bare repositories have no .git entry and are not repositories
// for Brooom. GIT_CEILING_DIRECTORIES is deliberately ignored: scope is
// decided by Brooom's own guard, not by git's discovery settings.
func FindRepoRoot(start string) (string, error) {
	resolved, err := resolveFull(start)
	if err != nil {
		return "", fmt.Errorf("scope: find repository from %q: %w", start, err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("scope: find repository from %q: %w", start, err)
	}
	dir := resolved
	if !info.IsDir() {
		dir = filepath.Dir(resolved)
	}
	for {
		if hasGitEntry(dir) {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("scope: %q: %w", start, ErrNotInRepo)
		}
		dir = parent
	}
}

// hasGitEntry reports whether dir directly contains a valid .git entry.
func hasGitEntry(dir string) bool {
	entry := filepath.Join(dir, ".git")
	info, err := os.Stat(entry) // follows a symlink to a directory
	if err != nil {
		return false
	}
	if info.IsDir() {
		return true
	}
	return info.Mode().IsRegular() && isGitdirFile(entry)
}

// isGitdirFile reports whether the file starts with a "gitdir:" pointer.
func isGitdirFile(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	head, err := io.ReadAll(io.LimitReader(f, gitFileProbeSize))
	if err != nil {
		return false
	}
	return bytes.HasPrefix(head, []byte("gitdir:"))
}
