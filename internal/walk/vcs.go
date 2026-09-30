package walk

import (
	"os"
	"path/filepath"
	"strings"
)

// vcsNames are the metadata entries that mark a directory as a version
// control checkout: git (directory, or file for linked worktrees and
// submodules), Mercurial, Jujutsu and Subversion. Anything holding one is
// somebody's repository and must never be treated as disposable clutter.
var vcsNames = []string{".git", ".hg", ".jj", ".svn"}

// VCSNames returns a copy of the VCS metadata entry names (.git, .hg, .jj,
// .svn) for callers that must probe for each of them, for example to find a
// sibling of a path that is an alias of one.
func VCSNames() []string { return append([]string(nil), vcsNames...) }

// IsVCSName reports whether an entry name is VCS metadata (.git, .hg, .jj or
// .svn). The comparison folds case on Windows and macOS, whose default
// filesystems do, so ".Git" cannot slip through there.
func IsVCSName(name string) bool {
	for _, n := range vcsNames {
		if nameEq(name, n) {
			return true
		}
	}
	return false
}

// nameEq compares entry names by the case rule of this OS's filesystem.
func nameEq(name, want string) bool {
	return name == want || (foldNames() && strings.EqualFold(name, want))
}

// DirShape collects the entry names of one directory that make up the shape of
// a bare git repository: a HEAD file plus objects/ and refs/ directories. A
// bare clone has no .git entry, so name based detection alone misses it. Add
// every entry of a directory, then ask IsBareRepo.
type DirShape struct {
	head, objects, refs bool
}

// Add records one entry of the directory. Symlinks never count (neither
// isDir nor isRegular is true for them), so the check does not follow links.
func (s *DirShape) Add(name string, isDir, isRegular bool) {
	switch {
	case isRegular && nameEq(name, "HEAD"):
		s.head = true
	case isDir && nameEq(name, "objects"):
		s.objects = true
	case isDir && nameEq(name, "refs"):
		s.refs = true
	}
}

// IsBareRepo reports whether the collected entries look like a bare git
// repository.
func (s DirShape) IsBareRepo() bool { return s.head && s.objects && s.refs }

// HasVCSEntry reports whether dir directly is a repository: it contains
// VCS metadata (see IsVCSName) or has the shape of a bare git repository.
// It uses Lstat so a symlinked entry is never followed. Detectors use it to
// leave nested repositories alone without a second traversal.
func HasVCSEntry(dir string) bool {
	for _, n := range vcsNames {
		if _, err := os.Lstat(filepath.Join(dir, n)); err == nil {
			return true
		}
	}
	var s DirShape
	for _, n := range []string{"HEAD", "objects", "refs"} {
		if fi, err := os.Lstat(filepath.Join(dir, n)); err == nil {
			s.Add(n, fi.IsDir(), fi.Mode().IsRegular())
		}
	}
	return s.IsBareRepo()
}
