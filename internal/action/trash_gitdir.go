package action

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/Tobias-Braun/brooom/internal/walk"
)

// maxGitLinkSize bounds how much of a ".git" link file or "commondir" file is
// read; real ones are a single short line.
const maxGitLinkSize = 4096

// refuseGitDir refuses path when it is, or lies inside, a git directory that
// insideVCSDir cannot recognise by name: a bare repository (a bare clone, or
// the ".bare" of the "proj/.bare + proj/.git" layout), the target of a ".git"
// link file (`--separate-git-dir`, linked worktrees) and the common dir a
// linked worktree's git dir points to. Every ancestor up to and including the
// allowed root is examined. Deleting objects, refs or hooks there corrupts
// the repository just like deleting them from ".git", so --force must not
// lift it. The returned error wraps ErrSkipped.
func refuseGitDir(env *Env, path string) error {
	for cur := path; ; cur = filepath.Dir(cur) {
		// The target itself being a bare repository is reported by the
		// dedicated "contains a bare git repository" check; only its
		// ancestors are this function's business.
		if cur != path && isBareShaped(cur) {
			return skipf("refusing to remove git metadata (%s is a bare repository) or anything inside it", filepath.Base(cur))
		}
		for _, g := range linkedGitDirs(cur) {
			if covers(g, path) {
				return skipf("refusing to remove git metadata (git directory %s) or anything inside it", g)
			}
		}
		if isVolumeRoot(cur) || (env.Guard != nil && env.Guard.IsAllowedRoot(cur)) {
			return nil
		}
	}
}

// isBareShaped reports whether dir has the shape of a git directory: a HEAD
// file plus objects/ and refs/ directories. Lstat is used so links are never
// followed.
func isBareShaped(dir string) bool {
	var s walk.DirShape
	for _, n := range []string{"HEAD", "objects", "refs"} {
		if fi, err := os.Lstat(filepath.Join(dir, n)); err == nil {
			s.Add(n, fi.IsDir(), fi.Mode().IsRegular())
		}
	}
	return s.IsBareRepo()
}

// linkedGitDirs returns the git directories dir's ".git" link file points at:
// the "gitdir:" target and, when that directory has a "commondir" file, the
// common dir it names. Anything unreadable or malformed yields nothing; the
// bare-shape check and the lexical checks remain for those cases.
func linkedGitDirs(dir string) []string {
	target := readGitLink(filepath.Join(dir, ".git"), "gitdir:")
	if target == "" {
		return nil
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(dir, target)
	}
	out := []string{filepath.Clean(target)}
	if common := readGitLink(filepath.Join(target, "commondir"), ""); common != "" {
		if !filepath.IsAbs(common) {
			common = filepath.Join(target, common)
		}
		out = append(out, filepath.Clean(common))
	}
	for _, g := range out {
		if r, err := filepath.EvalSymlinks(g); err == nil && r != g {
			out = append(out, r)
		}
	}
	return out
}

// readGitLink returns the first line of a small regular file with prefix
// stripped, or "" when it is not a regular file, is too large, lacks the
// prefix or cannot be read.
func readGitLink(file, prefix string) string {
	fi, err := os.Lstat(file)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > maxGitLinkSize {
		return ""
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return ""
	}
	line, _, _ := strings.Cut(string(data), "\n")
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, prefix) {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(line, prefix))
}
