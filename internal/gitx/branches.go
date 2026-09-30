package gitx

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Branch is a local branch and its relation to its upstream.
type Branch struct {
	// Name is the branch name without refs/heads/, e.g. "feat/x".
	Name string
	// Tip is the full SHA of the branch tip.
	Tip string
	// Upstream is the configured upstream in short form ("origin/feat/x"),
	// empty if none is configured.
	Upstream string
	// UpstreamRef is the full ref of the configured upstream, e.g.
	// "refs/remotes/origin/feat/x", or "refs/heads/main" when the upstream is
	// a local branch (remote "."). Empty if none is configured.
	UpstreamRef string
	// UpstreamGone is true when an upstream is configured but the remote
	// branch no longer exists (typically after `fetch --prune`).
	UpstreamGone bool
	// Track is git's own summary, e.g. "[ahead 1, behind 2]" or "[gone]".
	Track string
	// Date is the committer date of the tip.
	Date time.Time
	// WorktreePath is non-empty when the branch is checked out in any
	// worktree, including the main one.
	WorktreePath string
}

// UpstreamIsLocal reports whether the configured upstream is another local
// branch (`git branch --set-upstream-to=main`, remote "."). Such a branch was
// never pushed anywhere: its upstream is not a remote-tracking ref.
func (b Branch) UpstreamIsLocal() bool {
	return strings.HasPrefix(b.UpstreamRef, "refs/heads/")
}

// RemoteBranch is a remote-tracking branch such as "origin/feat/x".
type RemoteBranch struct {
	Name string
	Tip  string
	Date time.Time
}

// Field and record separators of the for-each-ref formats: NUL cannot occur
// in ref names or paths and a newline cannot occur in ref names.
const (
	fieldSep  = "\x00"
	branchFmt = "%(refname)%00%(objectname)%00%(upstream:short)%00%(upstream)%00%(upstream:track)%00%(committerdate:unix)%00%(worktreepath)"
	remoteFmt = "%(refname)%00%(objectname)%00%(committerdate:unix)%00%(symref)"
)

// ListBranches returns all local branches with one for-each-ref call. The
// name comes from %(refname) with refs/heads/ stripped because
// %(refname:short) may add a "heads/" prefix when a name is ambiguous.
func (r *Repo) ListBranches(ctx context.Context) ([]Branch, error) {
	return cached(r, &r.branches, struct{}{}, func() ([]Branch, error) {
		out, err := r.run(ctx, "for-each-ref", "--format="+branchFmt, "refs/heads/")
		if err != nil {
			return nil, err
		}
		branches, err := parseBranches(out)
		if err != nil {
			return nil, err
		}
		return r.markOperationBranches(branches), nil
	})
}

// markOperationBranches sets WorktreePath for a branch that a rebase or
// bisect in progress will return to. Such a branch is not checked out while
// the operation runs (HEAD is detached), so %(worktreepath) is empty for it,
// but git refuses to delete it ("used by worktree") and finishing the
// operation moves it. Treating it as checked out keeps every branch action
// away from it. The git directories are read from the file system (no extra
// git call, and a broken worktree registration cannot hide an operation).
func (r *Repo) markOperationBranches(branches []Branch) []Branch {
	if r.Common == "" {
		return branches
	}
	for _, dir := range WorktreeGitDirs(r.Common) {
		name := OperationBranch(dir)
		if name == "" {
			continue
		}
		if _, _, found := OperationInProgress(dir); !found {
			continue
		}
		for i := range branches {
			if branches[i].Name == name && branches[i].WorktreePath == "" {
				branches[i].WorktreePath = worktreeOfGitDir(r.Common, dir)
			}
		}
	}
	return branches
}

// worktreeOfGitDir returns the working directory that owns gitDir: the parent
// of the common directory for the main worktree, the target of the "gitdir"
// back-pointer for a linked one. The common directory itself is the fallback
// (bare-like layouts, unreadable pointer), so the result is never empty.
func worktreeOfGitDir(common, gitDir string) string {
	if gitDir != common {
		if ptr := readFirstLine(filepath.Join(gitDir, "gitdir")); ptr != "" {
			return NormalizePath(filepath.Dir(cleanNative(ptr)))
		}
		return gitDir
	}
	if filepath.Base(common) == ".git" {
		return filepath.Dir(common)
	}
	return common
}

func parseBranches(out string) ([]Branch, error) {
	var branches []Branch
	for _, line := range Lines(out) {
		f := strings.Split(line, fieldSep)
		if len(f) != 7 {
			return nil, fmt.Errorf("gitx: unexpected for-each-ref record %q", line)
		}
		b := Branch{
			Name:         strings.TrimPrefix(f[0], "refs/heads/"),
			Tip:          f[1],
			Upstream:     f[2],
			UpstreamRef:  f[3],
			UpstreamGone: strings.Contains(f[4], "gone"),
			Track:        f[4],
			Date:         parseUnix(f[5]),
		}
		if f[6] != "" {
			b.WorktreePath = NormalizePath(f[6])
		}
		branches = append(branches, b)
	}
	return branches, nil
}

// parseUnix parses a unix timestamp; malformed input yields the zero time.
func parseUnix(s string) time.Time {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return time.Time{}
	}
	return time.Unix(n, 0)
}

// ListRemoteBranches returns the remote-tracking branches under
// refs/remotes/, without symbolic refs such as origin/HEAD.
func (r *Repo) ListRemoteBranches(ctx context.Context) ([]RemoteBranch, error) {
	return cached(r, &r.remoteBranches, struct{}{}, func() ([]RemoteBranch, error) {
		out, err := r.run(ctx, "for-each-ref", "--format="+remoteFmt, "refs/remotes/")
		if err != nil {
			return nil, err
		}
		var res []RemoteBranch
		for _, line := range Lines(out) {
			f := strings.Split(line, fieldSep)
			if len(f) != 4 {
				return nil, fmt.Errorf("gitx: unexpected for-each-ref record %q", line)
			}
			if f[3] != "" {
				continue // symbolic ref such as origin/HEAD
			}
			res = append(res, RemoteBranch{
				Name: strings.TrimPrefix(f[0], "refs/remotes/"),
				Tip:  f[1],
				Date: parseUnix(f[2]),
			})
		}
		return res, nil
	})
}

// RemoteContaining answers "is the tip of ref reachable from some
// remote-tracking branch, and which one": it returns that branch (for
// example "origin/feat/x") or "" when none contains the commit. It is
// read-only and memoized per ref. An unresolvable ref is an error.
//
// The result is the first match in refname order. The symbolic
// refs/remotes/origin/HEAD sorts before origin/main and would shadow the real
// branch, so */HEAD results are skipped; when --count=1 only produced such an
// entry, the query is repeated without the limit to find the first real one.
func (r *Repo) RemoteContaining(ctx context.Context, ref string) (string, error) {
	return cached(r, &r.remoteHolder, ref, func() (string, error) {
		sha, err := r.resolveCommit(ctx, ref)
		if err != nil {
			return "", fmt.Errorf("gitx: cannot resolve %q: %w", ref, err)
		}
		out, err := r.run(ctx, "for-each-ref", "--count=1", "--contains", sha, "--format=%(refname)", "refs/remotes/")
		if err != nil {
			return "", err
		}
		if name := firstRealRemote(out); name != "" || out == "" {
			return name, nil
		}
		out, err = r.run(ctx, "for-each-ref", "--contains", sha, "--format=%(refname)", "refs/remotes/")
		if err != nil {
			return "", err
		}
		return firstRealRemote(out), nil
	})
}

// firstRealRemote returns the first ref of a refname listing that is not a
// remote HEAD, with refs/remotes/ stripped.
func firstRealRemote(out string) string {
	for _, l := range Lines(out) {
		if strings.HasSuffix(l, "/HEAD") {
			continue
		}
		return strings.TrimPrefix(l, "refs/remotes/")
	}
	return ""
}

// UnpushedCount returns how many commits reachable from ref are not
// contained in any remote-tracking branch (`rev-list --count ref --not
// --remotes`).
func (r *Repo) UnpushedCount(ctx context.Context, ref string) (int, error) {
	return cached(r, &r.unpushed, ref, func() (int, error) {
		out, err := r.run(ctx, "rev-list", "--count", ref, "--not", "--remotes")
		if err != nil {
			return 0, err
		}
		n, err := strconv.Atoi(strings.TrimSpace(out))
		if err != nil {
			return 0, fmt.Errorf("gitx: unexpected rev-list count %q", out)
		}
		return n, nil
	})
}

// UniqueCount returns how many commits are reachable from the branch but from
// no other local branch and no remote-tracking branch: what deleting the
// branch would make unreachable. UnpushedCount is the safety gate (commits on
// no remote); this one is the number to show a user, since commits that also
// sit on master or a sibling branch are not lost. The branch is given by
// name and its own ref is excluded from the comparison.
func (r *Repo) UniqueCount(ctx context.Context, branch string) (int, error) {
	// Ref names cannot contain glob characters, so the name is a literal
	// pattern for --exclude; with --branches it is relative to refs/heads/.
	out, err := r.run(ctx, "rev-list", "--count", "refs/heads/"+branch, "--not",
		"--exclude="+branch, "--branches", "--remotes")
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return 0, fmt.Errorf("gitx: unexpected rev-list count %q", out)
	}
	return n, nil
}

// OnlyOnBranchPhrase words a UniqueCount for messages, e.g. "3 commits exist
// only on this branch". Zero means every commit also sits on another local
// branch, which is why the branch is still not on any remote.
func OnlyOnBranchPhrase(n int) string {
	switch n {
	case 0:
		return "its commits are on no remote (they also exist on other local branches)"
	case 1:
		return "1 commit exists only on this branch"
	}
	return fmt.Sprintf("%d commits exist only on this branch", n)
}

// BranchCreatedOnly reports whether the reflog of the branch consists of at
// most its creation entry, i.e. nothing ever moved the branch since it was
// created. An empty or missing reflog (expired, disabled, branch created by
// plumbing) counts as true: the caller cannot tell the branch was ever used
// and must stay conservative. Only the reflog message of each entry is read.
func (r *Repo) BranchCreatedOnly(ctx context.Context, branch string) (bool, error) {
	out, err := r.run(ctx, "reflog", "show", "--format=%gs", "refs/heads/"+branch, "--")
	if err != nil {
		return false, err
	}
	entries := Lines(out)
	switch len(entries) {
	case 0:
		return true, nil
	case 1:
		return strings.HasPrefix(entries[0], "branch: Created"), nil
	}
	return false, nil
}

// ContainedInRemotes reports whether every commit of ref is contained in some
// remote-tracking branch, i.e. deleting ref loses nothing that is not also on
// a remote.
func (r *Repo) ContainedInRemotes(ctx context.Context, ref string) (bool, error) {
	n, err := r.UnpushedCount(ctx, ref)
	return err == nil && n == 0, err
}

// NeverPushed reports whether the branch has no remote upstream configured and
// no remote-tracking branch with the same name exists on any remote. An
// upstream that is another local branch is no evidence of a push.
func (r *Repo) NeverPushed(ctx context.Context, b Branch) (bool, error) {
	if b.Upstream != "" && !b.UpstreamIsLocal() {
		return false, nil
	}
	remotes, err := r.ListRemoteBranches(ctx)
	if err != nil {
		return false, err
	}
	for _, rb := range remotes {
		// The remote name is the first path segment: origin/feat/x -> feat/x.
		if _, short, ok := strings.Cut(rb.Name, "/"); ok && short == b.Name {
			return false, nil
		}
	}
	return true, nil
}

// CommitTime returns the committer time of a commit.
func (r *Repo) CommitTime(ctx context.Context, sha string) (time.Time, error) {
	out, err := r.run(ctx, "show", "-s", "--format=%ct", sha)
	if err != nil {
		return time.Time{}, err
	}
	t := parseUnix(out)
	if t.IsZero() {
		return t, fmt.Errorf("gitx: unexpected commit time %q", out)
	}
	return t, nil
}
