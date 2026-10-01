package gitx

import (
	"context"
	"errors"
	"path"
	"runtime"
	"slices"
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
	// Ref is the short form "origin/main" or a local "main". It is for
	// display only: git resolves tags and local branches before remote-tracking
	// refs, so a branch named "origin/main" would shadow it.
	Ref string
	// FullRef is the fully qualified ref ("refs/remotes/origin/main" or
	// "refs/heads/main") that every git call must use.
	FullRef string
	// Name is the branch name without remote, e.g. "main".
	Name string
	// Remote is "origin" when Ref is a remote-tracking ref, else empty.
	Remote string
	// Source says how the base was found (BaseSource* constants).
	Source string
	// Unpushed is set on a local candidate that ranks behind a remote primary
	// base: a merge into it exists only in this clone, so no remote has proof
	// of it. It is always false on the primary base.
	Unpushed bool
}

// Display is the base in the words shown to the user: the short ref, or
// "local main (not pushed)" for a local candidate behind a remote primary.
func (b Base) Display() string {
	if b.Unpushed {
		return "local " + b.Name + " (not pushed)"
	}
	return b.Ref
}

const originRemote = "origin"

// DefaultBase resolves the base ref: first the refs/remotes/origin/HEAD
// symbolic ref if it points at an existing ref, then for every configured
// name in order origin/<name> if it exists, else the local <name>. The remote
// ref is preferred because a stale local main would miss merges. It never
// runs `remote set-head` or `fetch`: no network, no writes. Only the remote
// "origin" is consulted.
func (r *Repo) DefaultBase(ctx context.Context, configured []string) (Base, error) {
	return r.sharedBases(&r.bases, strings.Join(configured, "\x00"), func() (Base, error) {
		refs, err := r.listBaseRefs(ctx, configured)
		if err != nil {
			return Base{}, err
		}
		return refs.primary(configured)
	})
}

// primary is DefaultBase answered from the listing.
func (b baseRefs) primary(configured []string) (Base, error) {
	if base, ok := b.originHeadBase(); ok {
		return base, nil
	}
	for _, name := range configured {
		if remote := remoteBase(name); b.has(remote.FullRef) {
			return remote, nil
		}
		if local := localBase(name); b.has(local.FullRef) {
			return local, nil
		}
	}
	return Base{}, ErrNoBase
}

// BaseCandidates returns every existing base ref merge detection may compare
// against, primary first (what DefaultBase returns), then origin/HEAD, then for
// every configured name origin/<name> and the local <name>. Preferring the
// remote base stays the default for safety decisions (a stale local main misses
// merges), but a branch merged only into a local main that is ahead of origin
// is still merged; such local candidates carry Unpushed so callers do not
// mistake them for remote-verified. Duplicates are dropped by full ref. The
// returned slice is shared and must not be modified.
func (r *Repo) BaseCandidates(ctx context.Context, configured []string) ([]Base, error) {
	return r.sharedCandidates(strings.Join(configured, "\x00"), func() ([]Base, error) {
		refs, err := r.listBaseRefs(ctx, configured)
		if err != nil {
			return nil, err
		}
		// The same listing answers DefaultBase, so both always agree.
		primary, err := refs.primary(configured)
		if err != nil {
			return nil, err
		}
		out := []Base{primary}
		seen := map[string]bool{primary.FullRef: true}
		add := func(b Base) {
			if seen[b.FullRef] || !refs.has(b.FullRef) {
				return
			}
			seen[b.FullRef] = true
			b.Unpushed = b.Remote == "" && primary.Remote != ""
			out = append(out, b)
		}
		if b, ok := refs.originHeadBase(); ok {
			add(b)
		}
		for _, name := range configured {
			add(remoteBase(name))
			add(localBase(name))
		}
		return out, nil
	})
}

// remoteBase is the configured name as a branch of origin.
func remoteBase(name string) Base {
	return Base{Ref: originRemote + "/" + name, FullRef: "refs/remotes/" + originRemote + "/" + name, Name: name, Remote: originRemote, Source: BaseSourceConfigRemote}
}

// localBase is the configured name as a local branch.
func localBase(name string) Base {
	return Base{Ref: name, FullRef: "refs/heads/" + name, Name: name, Source: BaseSourceConfigLocal}
}

// MergedIntoAny reports whether branch is merged into any of the bases, in
// order, and which base matched. Ancestry against every base is tried before
// any patch-id (squash/rebase) detection, so a cheap certain answer against a
// later candidate wins over an expensive guess against the first. A failing
// check is unknown and never merged; its error is returned only when no base
// matched.
func (r *Repo) MergedIntoAny(ctx context.Context, bases []Base, branch string, includeSquash bool) (Base, MergeResult, error) {
	var firstErr error
	modes := []bool{false}
	if includeSquash {
		modes = append(modes, true)
	}
	// notAncestor[i] records that the first pass found the branch not to be
	// an ancestor of bases[i], so the squash pass need not ask again.
	notAncestor := make([]bool, len(bases))
	for _, squash := range modes {
		for i, b := range bases {
			res, err := r.mergedIntoPass(ctx, b.FullRef, branch, squash, notAncestor[i])
			if !squash && err == nil {
				notAncestor[i] = !res.Merged
			}
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			if res.Merged {
				return b, res, nil
			}
		}
	}
	return Base{}, MergeResult{}, firstErr
}

// mergedIntoPass is one check of MergedIntoAny: MergedInto, or for a branch
// the ancestor pass already found not to be an ancestor of base, only the
// squash/rebase detection, memoized under the same key as MergedInto with
// squash (the answers agree once ancestry is ruled out).
func (r *Repo) mergedIntoPass(ctx context.Context, base, branch string, squash, notAncestor bool) (MergeResult, error) {
	if !squash || !notAncestor {
		return r.MergedInto(ctx, base, branch, squash)
	}
	return cached(r, &r.merged, mergeKey{base, branch, true}, func() (MergeResult, error) {
		res, err := r.SquashMerged(ctx, base, branch)
		if isMissingObject(err) {
			return MergeResult{}, nil
		}
		return res, err
	})
}

// originHead is the symbolic ref base detection follows first.
const originHead = "refs/remotes/" + originRemote + "/HEAD"

// baseRefs is what base detection knows about its candidate refs after one
// listing: the commit of every candidate that exists and the target of
// refs/remotes/origin/HEAD.
type baseRefs struct {
	// tips maps each existing candidate (full ref) to its commit.
	tips map[string]string
	// originHead is the ref origin/HEAD points to, "" when it is unset or
	// its target does not exist.
	originHead string
}

// has reports whether the fully qualified ref exists as a commit.
func (b baseRefs) has(ref string) bool {
	_, ok := b.tips[ref]
	return ok
}

// originHeadBase follows refs/remotes/origin/HEAD when it points at an
// existing remote-tracking branch.
func (b baseRefs) originHeadBase() (Base, bool) {
	prefix := "refs/remotes/" + originRemote + "/"
	if !strings.HasPrefix(b.originHead, prefix) {
		return Base{}, false
	}
	name := strings.TrimPrefix(b.originHead, prefix)
	return Base{Ref: originRemote + "/" + name, FullRef: b.originHead, Name: name, Remote: originRemote, Source: BaseSourceOriginHead}, true
}

// baseRefFormat lists name, type and object of a ref, the peeled type and
// object of an annotated tag, and the target of a symbolic ref.
const baseRefFormat = "--format=%(refname)%00%(objecttype)%00%(objectname)%00%(*objecttype)%00%(*objectname)%00%(symref)"

// listBaseRefs asks one `for-each-ref` for origin/HEAD and every configured
// candidate (origin/<name> and <name>), instead of one rev-parse per
// candidate. for-each-ref patterns also match refs below a name
// (refs/heads/main/x), so only exact names are kept. A ref counts when it
// resolves to a commit, like `rev-parse --verify <ref>^{commit}`; a dangling
// origin/HEAD is not listed by git at all and so counts as unset.
func (r *Repo) listBaseRefs(ctx context.Context, configured []string) (baseRefs, error) {
	return cached(r, &r.baseRefs, strings.Join(configured, "\x00"), func() (baseRefs, error) {
		want := baseRefNames(configured)
		out, err := r.run(ctx, append([]string{"for-each-ref", baseRefFormat}, want...)...)
		if err != nil {
			return baseRefs{}, err
		}
		return r.parseBaseRefs(ctx, out, want), nil
	})
}

// baseRefNames lists origin/HEAD and the remote and local ref of every
// configured name, without duplicates, in that order.
func baseRefNames(configured []string) []string {
	out := []string{originHead}
	seen := map[string]bool{originHead: true}
	for _, name := range configured {
		for _, ref := range []string{remoteBase(name).FullRef, localBase(name).FullRef} {
			if !seen[ref] {
				seen[ref] = true
				out = append(out, ref)
			}
		}
	}
	return out
}

// parseBaseRefs reads the baseRefFormat records of the wanted refs.
func (r *Repo) parseBaseRefs(ctx context.Context, out string, want []string) baseRefs {
	refs := baseRefs{tips: map[string]string{}}
	for _, line := range Lines(out) {
		f := strings.Split(line, "\x00")
		if len(f) != 6 || !slices.Contains(want, f[0]) {
			continue
		}
		sha, ok := r.peeledCommit(ctx, f[0], f[1], f[2], f[3], f[4])
		switch {
		case !ok:
			// Not a commit, so the ref does not count as existing.
		case f[0] != originHead:
			refs.tips[f[0]] = sha
		case f[5] != "":
			refs.originHead = f[5]
			refs.tips[f[5]] = sha
		}
	}
	return refs
}

// peeledCommit returns the commit a listed ref resolves to: the object itself
// when it is a commit, the peeled object of an annotated tag pointing at a
// commit, and for anything deeper (a tag of a tag) whatever rev-parse says.
func (r *Repo) peeledCommit(ctx context.Context, ref, typ, obj, peeledType, peeled string) (string, bool) {
	switch {
	case typ == "commit":
		return obj, true
	case typ == "tag" && peeledType == "commit":
		return peeled, true
	case typ == "tag":
		sha, err := r.resolveCommit(ctx, ref)
		return sha, err == nil
	}
	return "", false
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
