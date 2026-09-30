package gitx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
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

type mergeKey struct {
	base, branch string
	squash       bool
}

// IsAncestor reports whether ancestor is reachable from descendant. Both must
// be fully qualified refs, "HEAD" or commit SHAs (see requireQualified). Exit
// status 0 is true, 1 is false and anything else (bad ref, crash) an error
// that is never mapped to false.
func (r *Repo) IsAncestor(ctx context.Context, ancestor, descendant string) (bool, error) {
	if err := requireQualified(ancestor, descendant); err != nil {
		return false, err
	}
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

// ErrUnqualifiedRef is returned when a merge query is given a short ref name.
var ErrUnqualifiedRef = errors.New("gitx: ref is not fully qualified")

// requireQualified rejects short names such as "origin/main" or "rel". Git
// resolves refs/tags and refs/heads before refs/remotes, so a same-named
// branch or tag would silently answer for the intended ref and could make
// unmerged work look merged. Fully qualified refs, HEAD and commit SHAs are
// unambiguous.
func requireQualified(refs ...string) error {
	for _, ref := range refs {
		if strings.HasPrefix(ref, "refs/") || ref == "HEAD" || isSHA(ref) {
			continue
		}
		return fmt.Errorf("%w: %q", ErrUnqualifiedRef, ref)
	}
	return nil
}

// isSHA reports whether s is a full SHA-1 or SHA-256 object id.
func isSHA(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// MergedInto is the single entry point for "is branch merged into base": an
// ancestor check first, then, when includeSquash is set, squash and rebase
// detection. Results are memoized per (base, branch, includeSquash) on cached
// handles. An error means unknown and must be treated as not merged.
func (r *Repo) MergedInto(ctx context.Context, base, branch string, includeSquash bool) (MergeResult, error) {
	if err := requireQualified(base, branch); err != nil {
		return MergeResult{}, err
	}
	return cached(r, &r.merged, mergeKey{base, branch, includeSquash}, func() (MergeResult, error) {
		res, err := r.mergedInto(ctx, base, branch, includeSquash)
		if isMissingObject(err) {
			// A partial clone lacks objects and lazy fetching is off, so the
			// answer is unknown, which callers treat as not merged.
			return MergeResult{}, nil
		}
		return res, err
	})
}

func (r *Repo) mergedInto(ctx context.Context, base, branch string, includeSquash bool) (MergeResult, error) {
	ok, err := r.isMergedAncestor(ctx, base, branch)
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
}

// isMissingObject reports whether err is git refusing to fetch an object from
// a promisor remote, which is what a partial clone reports once lazy fetching
// is disabled ("lazy fetching disabled" since git 2.44, "promisor remote" in
// older versions). Only these messages mean "absent by design". Generic
// "unable to read", "bad object" or "missing blob" also appear on genuine
// corruption, which must surface as an error rather than be cached as "not
// merged".
func isMissingObject(err error) bool {
	var gerr *Error
	if !errors.As(err, &gerr) {
		return false
	}
	s := strings.ToLower(gerr.Stderr)
	for _, marker := range []string{"promisor", "lazy fetching disabled"} {
		if strings.Contains(s, marker) {
			return true
		}
	}
	return false
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
	if err := requireQualified(base, branch); err != nil {
		return MergeResult{}, err
	}
	baseSHA, err := r.resolveCommit(ctx, base)
	if err != nil {
		return MergeResult{}, err
	}
	tip, err := r.branchTip(ctx, branch)
	if err != nil {
		return MergeResult{}, err
	}
	mb, err := r.mergeBase(ctx, baseSHA, tip)
	if err != nil || mb == "" || mb == tip {
		// No common history, or the branch is an ancestor, which the
		// ancestor check owns.
		return MergeResult{}, err
	}
	set, tooBig, err := r.basePatchIDs(ctx, baseSHA, mb)
	if errors.Is(err, ErrInputUnsupported) {
		return MergeResult{}, nil
	}
	if err != nil {
		return MergeResult{}, err
	}
	if tooBig {
		return MergeResult{Truncated: true}, nil
	}
	if len(set) == 0 {
		// Nothing on base since the fork point can be equal to the branch,
		// so the diffs of the branch need not be computed at all.
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

// basePatchIDs returns the patch ids of the non-merge commits of base since
// the merge base. The commits are listed per branch (one cheap rev-list, capped
// at maxBaseCommits so a branch is only Truncated by its own distance from
// base), but the expensive part, the patch ids, is looked up in a per-commit
// cache and computed only for commits not seen before. A base commit is
// therefore diffed once per scan, however many distinct fork points the
// branches have, instead of once per fork point.
func (r *Repo) basePatchIDs(ctx context.Context, base, mb string) (ids map[string]struct{}, tooBig bool, err error) {
	out, err := r.run(ctx, "rev-list", "--no-merges", "--max-count="+strconv.Itoa(maxBaseCommits+1), base, "^"+mb)
	if err != nil {
		return nil, false, err
	}
	commits := Lines(out)
	if len(commits) > maxBaseCommits {
		return nil, true, nil
	}
	return r.commitPatchIDs(ctx, commits)
}

// matchBranch compares the branch against the base patch ids: combined diff
// first (squash), then commit by commit (rebase).
func (r *Repo) matchBranch(ctx context.Context, set map[string]struct{}, mb, tip string) (MergeResult, error) {
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
		if _, ok := set[combined[0]]; ok {
			return MergeResult{Merged: true, Method: MethodSquash}, nil
		}
	}
	return r.matchCommits(ctx, set, mb, tip)
}

// matchCommits is the rebase check: every non-merge commit of the branch has
// an equal patch id on base. It refuses branches containing merge commits,
// because the per-commit comparison skips them: whatever a merge commit adds
// or resolves would go unchecked and be lost with the branch. Such a branch
// can only match through its net diff (the squash check).
func (r *Repo) matchCommits(ctx context.Context, set map[string]struct{}, mb, tip string) (MergeResult, error) {
	merges, err := r.run(ctx, "rev-list", "--merges", "--max-count=1", mb+".."+tip)
	if err != nil {
		return MergeResult{}, err
	}
	if strings.TrimSpace(merges) != "" {
		return MergeResult{}, nil
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
		if _, ok := set[id]; !ok {
			return MergeResult{}, nil
		}
	}
	return MergeResult{Merged: true, Method: MethodRebase}, nil
}

// patchPair is one line of `git patch-id` output.
type patchPair struct{ id, commit string }

// limit returns the diff byte cap in force for r.
func (r *Repo) limit() int64 {
	if r.diffLimit > 0 {
		return r.diffLimit
	}
	return maxDiffBytes
}

// diffPatchIDs runs a diff-producing git command and pipes its output through
// `git patch-id --stable`, returning the patch ids in order. tooBig reports
// that the diff exceeded the cap. A patch-id failure is an error, which
// callers treat as unknown.
func (r *Repo) diffPatchIDs(ctx context.Context, args []string) (ids []string, tooBig bool, err error) {
	pairs, tooBig, err := r.diffPatchPairs(ctx, nil, args)
	for _, p := range pairs {
		ids = append(ids, p.id)
	}
	return ids, tooBig, err
}

// diffPatchPairs is diffPatchIDs keeping the commit id next to each patch id.
// A non-nil stdin is fed to the diff command (revisions for `log --stdin`).
//
// With a real git runner the producer is streamed into patch-id (PipeLimit),
// so memory stays bounded and both processes are killed at the cap instead of
// buffering a multi-hundred-megabyte diff first. Other runners (test fakes)
// buffer the diff and feed it through RunInput, which needs input support.
func (r *Repo) diffPatchPairs(ctx context.Context, stdin io.Reader, args []string) (pairs []patchPair, tooBig bool, err error) {
	if _, ok := r.Runner.(*ExecRunner); ok {
		return r.streamPatchPairs(ctx, stdin, args)
	}
	var diff string
	if stdin != nil {
		diff, err = RunInput(ctx, r.Runner, r.Dir, stdin, args...)
	} else {
		diff, err = r.run(ctx, args...)
	}
	if err != nil {
		return nil, false, err
	}
	if int64(len(diff)) > r.limit() {
		return nil, true, nil
	}
	if strings.TrimSpace(diff) == "" {
		return nil, false, nil
	}
	out, err := RunInput(ctx, r.Runner, r.Dir, strings.NewReader(diff), "patch-id", "--stable")
	if err != nil {
		return nil, false, err
	}
	return parsePatchPairs(out), false, nil
}

// streamPatchPairs is the streaming variant of diffPatchPairs.
func (r *Repo) streamPatchPairs(ctx context.Context, stdin io.Reader, args []string) ([]patchPair, bool, error) {
	var pairs []patchPair
	err := pipeLimitInput(ctx, r.Runner, r.Dir, stdin, args, []string{"patch-id", "--stable"}, r.limit(), func(line string) {
		pairs = append(pairs, parsePatchPairs(line)...)
	})
	if errors.Is(err, ErrOutputLimit) {
		return nil, true, nil
	}
	if err != nil {
		return nil, false, err
	}
	return pairs, false, nil
}

// parsePatchPairs extracts "patchid commitid" from every `patch-id` output
// line and drops all-zero ids, which git prints for diffs without content.
func parsePatchPairs(out string) []patchPair {
	var pairs []patchPair
	for _, line := range Lines(out) {
		id, rest, _ := strings.Cut(line, " ")
		if id == "" || strings.Trim(id, "0") == "" {
			continue
		}
		pairs = append(pairs, patchPair{id: id, commit: strings.TrimSpace(rest)})
	}
	return pairs
}

// patchCache maps a commit to its patch id ("" for a commit whose diff is
// empty), so every commit is diffed at most once per handle.
type patchCache struct {
	mu  sync.Mutex
	ids map[string]string
}

// commitPatchIDs returns the set of patch ids of commits, diffing only the
// commits missing from the cache (one `log -p --no-walk --stdin` for all of
// them). tooBig reports that the diff text of the missing commits exceeded
// maxDiffBytes; nothing is cached then.
func (r *Repo) commitPatchIDs(ctx context.Context, commits []string) (map[string]struct{}, bool, error) {
	var missing []string
	r.patches.mu.Lock()
	for _, c := range commits {
		if _, ok := r.patches.ids[c]; !ok {
			missing = append(missing, c)
		}
	}
	r.patches.mu.Unlock()
	if len(missing) > 0 {
		args := append([]string{"log", "-p", "--no-merges", "--no-walk=unsorted", "--stdin"}, diffFlags...)
		pairs, tooBig, err := r.diffPatchPairs(ctx, strings.NewReader(strings.Join(missing, "\n")+"\n"), args)
		if err != nil || tooBig {
			return nil, tooBig, err
		}
		r.storePatchIDs(missing, pairs)
	}
	set := make(map[string]struct{}, len(commits))
	r.patches.mu.Lock()
	defer r.patches.mu.Unlock()
	for _, c := range commits {
		if id := r.patches.ids[c]; id != "" {
			set[id] = struct{}{}
		}
	}
	return set, false, nil
}

// storePatchIDs records the computed ids; requested commits without a line in
// the output have an empty diff and are cached as "".
func (r *Repo) storePatchIDs(requested []string, pairs []patchPair) {
	r.patches.mu.Lock()
	defer r.patches.mu.Unlock()
	if r.patches.ids == nil {
		r.patches.ids = make(map[string]string)
	}
	for _, c := range requested {
		r.patches.ids[c] = ""
	}
	for _, p := range pairs {
		r.patches.ids[p.commit] = p.id
	}
}
