package gitx

import (
	"context"
	"errors"
	"strconv"
	"strings"
)

// Merge detection methods reported in MergeResult.Method.
const (
	MethodAncestor = "ancestor"
	MethodSquash   = "squash"
	MethodRebase   = "rebase"
)

// Safety caps for squash/rebase detection. Exceeding one makes the answer
// "not merged" (unknown is never treated as merged) with Truncated set.
const (
	// maxBaseCommits bounds how many commits of the base branch since the
	// merge base are scanned for equal patch ids.
	maxBaseCommits = 5000
	// maxDiffBytes bounds the diff text (per call) fed into patch-id.
	maxDiffBytes = 64 << 20
)

// diffFlags make diffs comparable and independent of user configuration:
// identical flags on the base and the branch side yield comparable ids.
var diffFlags = []string{"--no-renames", "--no-color", "--no-ext-diff", "--no-textconv"}

// MergeResult tells whether a branch counts as merged and how it was found.
type MergeResult struct {
	Merged bool
	// Method is "ancestor", "squash", "rebase" or "" (not merged / unknown).
	Method string
	// Truncated is set when a safety cap made the check give up.
	Truncated bool
}

type patchKey struct{ base, mergeBase string }

// patchSet is the set of patch ids of the base commits since the merge base.
type patchSet struct {
	ids       map[string]struct{}
	truncated bool
}

type mergeKey struct {
	base, branch string
	squash       bool
}

// IsAncestor reports whether ancestor is reachable from descendant. Exit
// status 0 is true, 1 is false and anything else (bad ref, crash) an error
// that is never mapped to false.
func (r *Repo) IsAncestor(ctx context.Context, ancestor, descendant string) (bool, error) {
	_, err := r.run(ctx, "merge-base", "--is-ancestor", ancestor, descendant)
	if err == nil {
		return true, nil
	}
	var gerr *Error
	if errors.As(err, &gerr) && gerr.ExitCode == 1 {
		return false, nil
	}
	return false, err
}

// MergedInto is the single entry point for "is branch merged into base": an
// ancestor check first, then, when includeSquash is set, squash and rebase
// detection. Results are memoized per (base, branch, includeSquash) on cached
// handles. An error means unknown and must be treated as not merged.
func (r *Repo) MergedInto(ctx context.Context, base, branch string, includeSquash bool) (MergeResult, error) {
	return cached(r, &r.merged, mergeKey{base, branch, includeSquash}, func() (MergeResult, error) {
		ok, err := r.IsAncestor(ctx, branch, base)
		if err != nil {
			return MergeResult{}, err
		}
		if ok {
			return MergeResult{Merged: true, Method: MethodAncestor}, nil
		}
		if !includeSquash {
			return MergeResult{}, nil
		}
		return r.SquashMerged(ctx, base, branch)
	})
}

// SquashMerged detects squash and rebase merges read-only with
// `git patch-id --stable`. Method "squash" means the combined diff
// merge-base..branch equals the patch of one commit on base; "rebase" means
// every non-empty, non-merge commit of the branch has an equal patch id on
// base. It never creates commits, trees or refs.
//
// Known limitations, all in the safe direction: when the base moved so that
// context lines differ the patch id changes (false negative); patch-id
// ignores whitespace-only differences (so such a squash is still found).
// Exceeding the caps (5000 base commits, 64 MiB of diff text) or a patch-id
// failure reports not merged with Truncated set where applicable. Callers
// normally use MergedInto, which checks ancestry first.
func (r *Repo) SquashMerged(ctx context.Context, base, branch string) (MergeResult, error) {
	baseSHA, err := r.resolveCommit(ctx, base)
	if err != nil {
		return MergeResult{}, err
	}
	tip, err := r.resolveCommit(ctx, branch)
	if err != nil {
		return MergeResult{}, err
	}
	mb, err := r.mergeBase(ctx, baseSHA, tip)
	if err != nil || mb == "" || mb == tip {
		// No common history, or the branch is an ancestor, which the
		// ancestor check owns.
		return MergeResult{}, err
	}
	set, err := cached(r, &r.patchIDs, patchKey{baseSHA, mb}, func() (patchSet, error) {
		return r.basePatchIDs(ctx, baseSHA, mb)
	})
	if errors.Is(err, ErrInputUnsupported) {
		return MergeResult{}, nil
	}
	if err != nil {
		return MergeResult{}, err
	}
	if set.truncated {
		return MergeResult{Truncated: true}, nil
	}
	if len(set.ids) == 0 {
		return MergeResult{}, nil
	}
	return r.matchBranch(ctx, set, mb, tip)
}

// mergeBase returns the merge base of a and b, or "" when they share no
// history (git exits 1 then).
func (r *Repo) mergeBase(ctx context.Context, a, b string) (string, error) {
	out, err := r.run(ctx, "merge-base", a, b)
	if err != nil {
		var gerr *Error
		if errors.As(err, &gerr) && gerr.ExitCode == 1 {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// basePatchIDs collects the patch ids of the non-merge commits of base since
// the merge base.
func (r *Repo) basePatchIDs(ctx context.Context, base, mb string) (patchSet, error) {
	out, err := r.run(ctx, "rev-list", "--count", base, "^"+mb)
	if err != nil {
		return patchSet{}, err
	}
	if n := countOr(out, maxBaseCommits+1); n > maxBaseCommits {
		return patchSet{truncated: true}, nil
	}
	args := append([]string{"log", "-p", "--no-merges"}, diffFlags...)
	args = append(args, base, "^"+mb)
	ids, tooBig, err := r.diffPatchIDs(ctx, args)
	if err != nil || tooBig {
		return patchSet{truncated: tooBig}, err
	}
	set := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		set[id] = struct{}{}
	}
	return patchSet{ids: set}, nil
}

// matchBranch compares the branch against the base patch ids: combined diff
// first (squash), then commit by commit (rebase).
func (r *Repo) matchBranch(ctx context.Context, set patchSet, mb, tip string) (MergeResult, error) {
	diffArgs := append([]string{"diff"}, diffFlags...)
	diffArgs = append(diffArgs, mb, tip)
	combined, tooBig, err := r.diffPatchIDs(ctx, diffArgs)
	if err != nil {
		return MergeResult{}, err
	}
	if tooBig {
		return MergeResult{Truncated: true}, nil
	}
	if len(combined) == 1 {
		if _, ok := set.ids[combined[0]]; ok {
			return MergeResult{Merged: true, Method: MethodSquash}, nil
		}
	}
	logArgs := append([]string{"log", "-p", "--no-merges"}, diffFlags...)
	logArgs = append(logArgs, mb+".."+tip)
	perCommit, tooBig, err := r.diffPatchIDs(ctx, logArgs)
	if err != nil {
		return MergeResult{}, err
	}
	if tooBig {
		return MergeResult{Truncated: true}, nil
	}
	if len(perCommit) == 0 {
		return MergeResult{}, nil
	}
	for _, id := range perCommit {
		if _, ok := set.ids[id]; !ok {
			return MergeResult{}, nil
		}
	}
	return MergeResult{Merged: true, Method: MethodRebase}, nil
}

// diffPatchIDs runs a diff-producing git command and pipes its output through
// `git patch-id --stable`, returning the patch ids in order. tooBig reports
// that the diff exceeded maxDiffBytes. A patch-id failure is an error, which
// callers treat as unknown.
func (r *Repo) diffPatchIDs(ctx context.Context, args []string) (ids []string, tooBig bool, err error) {
	diff, err := r.run(ctx, args...)
	if err != nil {
		return nil, false, err
	}
	if len(diff) > maxDiffBytes {
		return nil, true, nil
	}
	if strings.TrimSpace(diff) == "" {
		return nil, false, nil
	}
	out, err := RunInput(ctx, r.Runner, r.Dir, strings.NewReader(diff), "patch-id", "--stable")
	if err != nil {
		return nil, false, err
	}
	return parsePatchIDs(out), false, nil
}

// parsePatchIDs extracts the first field of every `patch-id` output line and
// drops all-zero ids, which git prints for diffs without content.
func parsePatchIDs(out string) []string {
	var ids []string
	for _, line := range Lines(out) {
		id, _, _ := strings.Cut(line, " ")
		if id == "" || strings.Trim(id, "0") == "" {
			continue
		}
		ids = append(ids, id)
	}
	return ids
}

// countOr parses a count printed by git, returning def when malformed so an
// unparseable count is treated as "over the cap" by the caller.
func countOr(s string, def int) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return def
	}
	return n
}
