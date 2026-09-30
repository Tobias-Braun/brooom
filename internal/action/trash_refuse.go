package action

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/scope"
	"github.com/Tobias-Braun/brooom/internal/walk"
)

// foldPaths reports whether path comparison is case-insensitive on this OS
// (the default filesystems of Windows and macOS). Comparing case-sensitively
// there would let "Repo" slip past a check written against "repo".
func foldPaths() bool { return runtime.GOOS == "windows" || runtime.GOOS == "darwin" }

// covers reports whether outer equals inner or inner lies below outer. Both
// must be clean and absolute; the check is lexical, so callers pass resolved
// paths.
func covers(outer, inner string) bool {
	if foldPaths() {
		outer, inner = strings.ToLower(outer), strings.ToLower(inner)
	}
	return outer == inner || findings.IsWithin(outer, inner)
}

// isGitName reports whether a path element is a git metadata entry. The
// comparison always folds case: refusing an oddly cased ".GIT" on a
// case-sensitive filesystem costs nothing, missing one on macOS would.
func isGitName(name string) bool { return strings.EqualFold(name, ".git") }

// insideVCSDir reports whether path is a VCS metadata entry (.git, .hg, .jj,
// .svn; walk.IsVCSName) or lies below one. .git additionally always folds
// case via isGitName, the other names follow the filesystem's case rule.
func insideVCSDir(path string) bool {
	vol := filepath.VolumeName(path)
	for _, part := range strings.Split(path[len(vol):], string(filepath.Separator)) {
		if isGitName(part) || walk.IsVCSName(part) {
			return true
		}
	}
	return false
}

// isVolumeRoot reports whether path is a filesystem root, a drive root or a
// UNC share root: filepath.Dir of those is the path itself.
func isVolumeRoot(path string) bool { return filepath.Dir(path) == path }

// protectedPaths returns the directories that must never be removed together
// with everything that contains them, keyed by a description for the skip
// reason: the Brooom home (config, cache, sessions, quarantine) and the
// user's home directory. Removing an ancestor of either destroys it too.
// Lookup failures leave the entry out: they mean the environment has no such
// location, so there is nothing to protect.
func protectedPaths() map[string]string {
	out := map[string]string{}
	if d, err := config.ResolveDirs(); err == nil {
		out[d.Home] = "the Brooom home"
	}
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		out[h] = "the home directory"
	}
	// Symlinked homes (macOS /var, /home on some Linux setups) must also be
	// protected under their resolved spelling, because the target path is
	// already resolved.
	for p, why := range out {
		if r, err := filepath.EvalSymlinks(p); err == nil && r != p {
			out[r] = why
		}
	}
	return out
}

// brooomStateDirs returns the parts of the Brooom home that are off limits
// even when they are only a target's ancestor: the manifests and the
// quarantine hold the only undo information.
func brooomStateDirs() []string {
	d, err := config.ResolveDirs()
	if err != nil {
		return nil
	}
	dirs := []string{d.Sessions, d.Quarantine}
	for _, p := range dirs {
		if r, err := filepath.EvalSymlinks(p); err == nil && r != p {
			dirs = append(dirs, r)
		}
	}
	return dirs
}

// brooomHomeDirs returns the whole Brooom home (config, cache, sessions,
// quarantine) in its configured and resolved spelling. Unlike
// brooomStateDirs it also covers config and cache: a restored file there
// could replace the configuration Brooom trusts.
func brooomHomeDirs() []string {
	d, err := config.ResolveDirs()
	if err != nil {
		return nil
	}
	dirs := []string{d.Home}
	if r, err := filepath.EvalSymlinks(d.Home); err == nil && r != d.Home {
		dirs = append(dirs, r)
	}
	return dirs
}

// refuseTarget applies the static refusals of the trash action to a resolved
// path. They depend on the path alone and are never overridable by --force,
// because acting on any of them is the highest-damage mistake there is:
// removing a scan root, a repository, git metadata, Brooom's own state, a
// filesystem root or the user's home. The returned error wraps ErrSkipped.
func refuseTarget(env *Env, path string) error {
	return refusePath(env, path, true)
}

// refusePath is refuseTarget with the repository-root refusal optional: a
// linked worktree root looks like a repository root (it has a .git link file)
// but is exactly what the worktree actions remove.
func refusePath(env *Env, path string, refuseRepoRoot bool) error {
	switch {
	case isVolumeRoot(path):
		return skipf("refusing to remove a filesystem root")
	case env.Guard.IsAllowedRoot(path):
		return skipf("refusing to remove an allowed root")
	case insideVCSDir(path):
		return skipf("refusing to remove VCS metadata (.git, .hg, .jj, .svn) or anything inside it")
	case refuseRepoRoot && isRepoRoot(path):
		return skipf("refusing to remove a repository root")
	}
	if err := refuseGitDir(env, path); err != nil {
		return err
	}
	if err := refuseBrooomAndHome(path); err != nil {
		return err
	}
	return RefuseByIdentity(path)
}

// refuseBrooomAndHome refuses the home directories and Brooom's own state,
// including any directory that contains them.
func refuseBrooomAndHome(path string) error {
	for p, why := range protectedPaths() {
		if covers(path, p) {
			return skipf("refusing to remove %s or a directory containing it", why)
		}
	}
	for _, p := range brooomStateDirs() {
		if covers(p, path) {
			return skipf("refusing to remove Brooom's own session or quarantine data")
		}
	}
	return nil
}

// isRepoRoot reports whether path is itself the top of a git repository.
// Resolution errors (for example a path that is already gone) mean "no
// repository here"; the later stat reports the real state.
func isRepoRoot(path string) bool {
	root, err := scope.FindRepoRoot(path)
	if err != nil {
		return false
	}
	return covers(root, path) && covers(path, root)
}

// skipf builds an error wrapping ErrSkipped with a formatted reason, the
// shape the executor unwraps with skipReason.
func skipf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrSkipped, fmt.Sprintf(format, args...))
}

// resolveTarget resolves the parent of a finding path through the guard and
// keeps the final element, so a symlink is removed as a link and never
// followed. Anything outside the allowed roots is a hard refusal.
func resolveTarget(env *Env, path string) (string, error) {
	if env.Guard == nil {
		return "", errors.New("trash: no scope guard configured")
	}
	resolved, err := env.Guard.ResolveParent(path)
	switch {
	case err == nil:
		return resolved, nil
	case errors.Is(err, scope.ErrOutsideScope):
		return "", skipf("outside allowed roots")
	default:
		return "", skipf("cannot resolve path: %v", err)
	}
}

// restoreForbiddenDirs lists every Brooom directory a restore must not write
// into: the state directories and the whole home with config and cache.
func restoreForbiddenDirs() []string {
	return append(brooomStateDirs(), brooomHomeDirs()...)
}

// refuseRestoreTarget applies the static refusals that matter for writing:
// a restore destination must never be git metadata or Brooom's own session or
// quarantine data, since a forged manifest could otherwise plant hooks or
// rewrite the undo information itself. The check is lexical first and then
// identity based (os.SameFile on every existing ancestor), because a name
// comparison alone misses aliases such as Windows 8.3 short names and
// symlinks. Known limit: identity is compared per ancestor of dest, so a bind
// mount or hard link of .git (or of a state directory) that is reached under
// a different parent is only caught when the aliased directory itself is an
// ancestor of dest or has a sibling ".git" entry; the scope guard's allowed
// roots remain the defence for everything else. The error is not a skip: a
// manifest asking for this is corrupt or forged.
func refuseRestoreTarget(dest string) error {
	if insideVCSDir(dest) {
		return fmt.Errorf("trash undo: refusing to restore to %s: inside VCS metadata (.git, .hg, .jj, .svn)", dest)
	}
	for _, p := range restoreForbiddenDirs() {
		if covers(p, dest) {
			return fmt.Errorf("trash undo: refusing to restore to %s: inside Brooom's own data (home, sessions or quarantine)", dest)
		}
	}
	return refuseRestoreByIdentity(dest)
}

// refuseRestoreByIdentity walks the existing ancestors of dest and compares
// each with the Brooom state directories and with its sibling ".git" entry by
// file identity. Missing path elements are skipped: they cannot alias
// anything yet.
func refuseRestoreByIdentity(dest string) error {
	var state []fileID
	for _, p := range restoreForbiddenDirs() {
		if id, err := identityOf(p, true); err == nil {
			state = append(state, id)
		}
	}
	for cur := dest; ; cur = filepath.Dir(cur) {
		if err := refuseAncestorIdentity(dest, cur, state); err != nil {
			return err
		}
		if isVolumeRoot(cur) {
			return nil
		}
	}
}

// refuseAncestorIdentity compares one existing ancestor of dest with the
// state directories and its sibling ".git". An ancestor whose identity cannot
// be read although it exists is unknown and refuses, as everywhere else; a
// missing ancestor cannot alias anything yet.
func refuseAncestorIdentity(dest, cur string, state []fileID) error {
	if !statable(cur) {
		return nil
	}
	id, err := identityOf(cur, true)
	if err != nil {
		if isAbsent(err) {
			return nil
		}
		return fmt.Errorf("trash undo: refusing to restore to %s: cannot read the identity of %s", dest, cur)
	}
	for _, s := range state {
		if id.sameAs(s) {
			return fmt.Errorf("trash undo: refusing to restore to %s: inside Brooom's own data (home, sessions or quarantine)", dest)
		}
	}
	gitPath := filepath.Join(filepath.Dir(cur), ".git")
	if !statable(gitPath) {
		return nil
	}
	if g, err := identityOf(gitPath, true); err != nil || id.sameAs(g) {
		return fmt.Errorf("trash undo: refusing to restore to %s: inside .git", dest)
	}
	return nil
}

// statable reports whether path exists and can be stat'ed. Callers use it to
// skip missing path elements, which cannot alias anything yet.
func statable(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
