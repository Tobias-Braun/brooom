package gitx

import (
	"context"
	"errors"
	"path"
	"runtime"
	"strings"
)

// ErrNoBase is returned by DefaultBase when no base branch can be resolved.
var ErrNoBase = errors.New("gitx: no base branch found")

// Sources describing how a Base was resolved.
const (
	BaseSourceOriginHead   = "origin-head"
	BaseSourceConfigRemote = "configured-remote"
	BaseSourceConfigLocal  = "configured-local"
)

// Base is the resolved base branch merge detection compares against.
type Base struct {
	// Ref is the ref to compare against: "origin/main" or a local "main".
	Ref string
	// Name is the branch name without remote, e.g. "main".
	Name string
	// Remote is "origin" when Ref is a remote-tracking ref, else empty.
	Remote string
	// Source says how the base was found (BaseSource* constants).
	Source string
}

const originRemote = "origin"

// DefaultBase resolves the base ref: first the refs/remotes/origin/HEAD
// symbolic ref if it points at an existing ref, then for every configured
// name in order origin/<name> if it exists, else the local <name>. The remote
// ref is preferred because a stale local main would miss merges. It never
// runs `remote set-head` or `fetch`: no network, no writes. Only the remote
// "origin" is consulted.
func (r *Repo) DefaultBase(ctx context.Context, configured []string) (Base, error) {
	return cached(r, &r.bases, strings.Join(configured, "\x00"), func() (Base, error) {
		if b, ok := r.originHeadBase(ctx); ok {
			return b, nil
		}
		for _, name := range configured {
			if r.refExists(ctx, "refs/remotes/"+originRemote+"/"+name) {
				return Base{Ref: originRemote + "/" + name, Name: name, Remote: originRemote, Source: BaseSourceConfigRemote}, nil
			}
			if r.refExists(ctx, "refs/heads/"+name) {
				return Base{Ref: name, Name: name, Source: BaseSourceConfigLocal}, nil
			}
		}
		return Base{}, ErrNoBase
	})
}

// originHeadBase follows refs/remotes/origin/HEAD when it points at an
// existing remote-tracking branch.
func (r *Repo) originHeadBase(ctx context.Context) (Base, bool) {
	target, err := r.run(ctx, "symbolic-ref", "--quiet", "refs/remotes/"+originRemote+"/HEAD")
	if err != nil {
		return Base{}, false
	}
	target = strings.TrimSpace(target)
	prefix := "refs/remotes/" + originRemote + "/"
	if !strings.HasPrefix(target, prefix) || !r.refExists(ctx, target) {
		return Base{}, false
	}
	name := strings.TrimPrefix(target, prefix)
	return Base{Ref: originRemote + "/" + name, Name: name, Remote: originRemote, Source: BaseSourceOriginHead}, true
}

// IsBaseBranch reports whether the local branch name is a base branch: the
// local counterpart of the resolved base or any name of the configured list.
// Such branches are never candidates for cleanup.
func IsBaseBranch(base Base, configured []string, name string) bool {
	if base.Name != "" && name == base.Name {
		return true
	}
	for _, c := range configured {
		if c == name {
			return true
		}
	}
	return false
}

// IsProtected reports whether branch matches one of the glob patterns. Rules:
// matching is case sensitive; `*` and `?` do not cross '/', so "release/*"
// matches "release/1.0" but not "release/1.0/hotfix"; a trailing "/**"
// matches any depth below the prefix; a malformed pattern falls back to
// literal equality so a typo can only protect less, never panic.
func IsProtected(patterns []string, branch string) bool {
	for _, p := range patterns {
		if matchPattern(p, branch) {
			return true
		}
	}
	return false
}

func matchPattern(pattern, branch string) bool {
	if prefix, ok := strings.CutSuffix(pattern, "/**"); ok {
		return strings.HasPrefix(branch, prefix+"/")
	}
	ok, err := path.Match(pattern, branch)
	if err != nil {
		return pattern == branch
	}
	return ok
}

// SamePath reports whether two paths denote the same location by comparing
// cleaned strings, case-insensitively on Windows and macOS whose default
// filesystems are case-insensitive. It never touches the filesystem, so it
// also works for missing directories.
func SamePath(a, b string) bool {
	return samePathOS(runtime.GOOS, a, b)
}

func samePathOS(goos, a, b string) bool {
	a, b = cleanNative(a), cleanNative(b)
	if goos == "windows" || goos == "darwin" {
		return strings.EqualFold(a, b)
	}
	return a == b
}
