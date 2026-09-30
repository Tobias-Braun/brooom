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

// insideGitDir reports whether path is a .git entry or lies below one.
func insideGitDir(path string) bool {
	vol := filepath.VolumeName(path)
	for _, part := range strings.Split(path[len(vol):], string(filepath.Separator)) {
		if isGitName(part) {
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

// refuseTarget applies the static refusals of the trash action to a resolved
// path. They depend on the path alone and are never overridable by --force,
// because acting on any of them is the highest-damage mistake there is:
// removing a scan root, a repository, git metadata, Brooom's own state, a
// filesystem root or the user's home. The returned error wraps ErrSkipped.
func refuseTarget(env *Env, path string) error {
	switch {
	case isVolumeRoot(path):
		return skipf("refusing to remove a filesystem root")
	case env.Guard.IsAllowedRoot(path):
		return skipf("refusing to remove an allowed root")
	case insideGitDir(path):
		return skipf("refusing to remove .git or anything inside it")
	case isRepoRoot(path):
		return skipf("refusing to remove a repository root")
	}
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
