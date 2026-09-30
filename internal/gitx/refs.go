package gitx

import (
	"context"
	"strings"
)

// listedRefs maps every local and remote-tracking branch (full ref name) to
// its tip. It reuses the memoized branch listings the detectors need anyway,
// so it costs no extra git call.
func (r *Repo) listedRefs(ctx context.Context) (map[string]string, error) {
	return cached(r, &r.refTips, struct{}{}, func() (map[string]string, error) {
		branches, err := r.ListBranches(ctx)
		if err != nil {
			return nil, err
		}
		remotes, err := r.ListRemoteBranches(ctx)
		if err != nil {
			return nil, err
		}
		tips := make(map[string]string, len(branches)+len(remotes))
		for _, b := range branches {
			tips["refs/heads/"+b.Name] = b.Tip
		}
		for _, rb := range remotes {
			tips["refs/remotes/"+rb.Name] = rb.Tip
		}
		return tips, nil
	})
}

// lookupBranch finds name among the listed branches, preferring a local
// branch over a remote-tracking one like git does. It only answers on cached
// handles, where the listing is a stable snapshot; uncached handles (actions)
// always ask git.
func (r *Repo) lookupBranch(ctx context.Context, name string) (full, tip string, ok bool) {
	if !r.memoize {
		return "", "", false
	}
	tips, err := r.listedRefs(ctx)
	if err != nil {
		return "", "", false
	}
	candidates := []string{name}
	if !strings.HasPrefix(name, "refs/") {
		candidates = []string{"refs/heads/" + name, "refs/remotes/" + name}
	}
	for _, c := range candidates {
		if tip, ok := tips[c]; ok {
			return c, tip, true
		}
	}
	return "", "", false
}

// branchTip returns the commit a branch points to, from the branch listing
// when possible and from rev-parse otherwise.
func (r *Repo) branchTip(ctx context.Context, name string) (string, error) {
	if _, tip, ok := r.lookupBranch(ctx, name); ok {
		return tip, nil
	}
	return r.resolveCommit(ctx, name)
}

// isMergedAncestor answers "is branch an ancestor of base". On cached handles
// one `for-each-ref --merged=<base>` per base answers it for every branch;
// otherwise, and whenever the batch cannot answer, it is one
// `merge-base --is-ancestor` per branch.
func (r *Repo) isMergedAncestor(ctx context.Context, base, branch string) (bool, error) {
	if full, _, ok := r.lookupBranch(ctx, branch); ok {
		if set, err := r.mergedRefSet(ctx, base); err == nil {
			_, merged := set[full]
			return merged, nil
		}
	}
	return r.IsAncestor(ctx, branch, base)
}

// mergedRefSet lists the branches (full refs) whose tips are reachable from
// base. base is pinned to its commit sha first so the query cannot be
// confused by ambiguous names.
func (r *Repo) mergedRefSet(ctx context.Context, base string) (map[string]struct{}, error) {
	sha, err := r.resolveCommit(ctx, base)
	if err != nil {
		return nil, err
	}
	return cached(r, &r.mergedRefs, sha, func() (map[string]struct{}, error) {
		out, err := r.run(ctx, "for-each-ref", "--merged="+sha, "--format=%(refname)", "refs/heads/", "refs/remotes/")
		if err != nil {
			return nil, err
		}
		set := make(map[string]struct{})
		for _, l := range Lines(out) {
			set[l] = struct{}{}
		}
		return set, nil
	})
}
