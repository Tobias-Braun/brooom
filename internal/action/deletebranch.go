package action

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/gitx"
	"github.com/Tobias-Braun/brooom/internal/session"
)

const (
	metaTip = "tip"
	// Values of the suggested action's "verified" argument set by the
	// detectors: which fact made the deletion safe at scan time.
	verifiedSquash   = "squash"
	verifiedInRemote = "in-remote"

	flagSafe  = "-d"
	flagForce = "-D"

	// notFullyMerged is the stderr fragment of `git branch -d` refusing an
	// unmerged branch. Git runs in the C locale (gitx.Env), so it is stable.
	notFullyMerged = "not fully merged"
)

// ghRunner runs gh for the open-PR re-check. It is a variable only so tests
// can inject a fake; nil means the gh binary on PATH.
var ghRunner gitx.GHRunner

// commitSHA matches a full or abbreviated commit id as recorded in the
// manifest; anything else (options, revision expressions) is refused on undo.
var commitSHA = regexp.MustCompile(`^[0-9a-fA-F]{7,64}$`)

// deleteBranch removes local branches found by the merged-branch and
// stale-branch detectors. Branches are deleted with `git branch -d`; -D is
// used only when the merge (ancestry, squash, rebase) or remote containment
// is re-verified at apply time, or with --force, because git itself cannot
// see squash merges or merges into a base that is not the checked-out HEAD.
type deleteBranch struct{}

func init() { Register(deleteBranch{}) }

// Type implements Action.
func (deleteBranch) Type() findings.ActionType { return findings.ActionDeleteBranch }

// decision is the outcome of evaluating a finding against the live
// repository. Plan and Apply share it so the dry run shows what will run.
type decision struct {
	repo *gitx.Repo
	cfg  *config.Config
	name string
	tip  string
	flag string
	// verified is the finding's claim of how the deletion was verified.
	verified string
	// why says what justified the flag, for the step description.
	why string
}

// command is the exact git invocation for display.
func (d decision) command() string {
	return "git branch " + d.flag + " " + shellQuote(d.name)
}

// Plan re-validates the finding against the live repository; see evaluate.
func (deleteBranch) Plan(ctx context.Context, env *Env, f findings.Finding) (Step, error) {
	d, err := evaluate(ctx, env, f)
	if err != nil {
		return Step{}, err
	}
	desc := fmt.Sprintf("delete branch %s with %s", d.name, d.flag)
	if d.why != "" {
		desc += " (" + d.why + ")"
	}
	return Step{Finding: f, Description: desc, Command: d.command()}, nil
}

// Apply evaluates again, because the repository may have changed since Plan,
// and runs git branch. A skip at this point is reported as a skipped entry.
func (deleteBranch) Apply(ctx context.Context, env *Env, s Step) (session.Entry, error) {
	f := s.Finding
	en := session.Entry{
		FindingID: f.ID, Detector: f.Detector, Action: findings.ActionDeleteBranch,
		Path: f.Path, Ref: f.Ref, At: time.Now().UTC(),
	}
	d, err := evaluate(ctx, env, f)
	if errors.Is(err, ErrSkipped) {
		en.Status, en.Error = session.StatusSkipped, skipReason(err)
		return en, nil
	}
	if err != nil {
		return failedBranch(en, err)
	}
	en.Path = d.repo.Dir
	if err := d.run(ctx, env); err != nil {
		if errors.Is(err, ErrSkipped) {
			en.Status, en.Error = session.StatusSkipped, skipReason(err)
			return en, nil
		}
		return failedBranch(en, err)
	}
	en.Status = session.StatusApplied
	en.Undo = map[string]string{"branch": d.name, "sha": d.tip}
	en.Restorable = true
	en.RecoveryHint = branchRecoveryHint(d.name, d.tip)
	return en, nil
}

func failedBranch(en session.Entry, err error) (session.Entry, error) {
	en.Status, en.Error = session.StatusFailed, err.Error()
	return en, err
}

// branchRecoveryHint is accurate about git: deleting a branch also deletes its own
// reflog, so the commits are only unreachable objects that a later gc prunes.
func branchRecoveryHint(name, sha string) string {
	return fmt.Sprintf("run inside the repository: git branch %s %s. Deleting a branch also deletes its reflog; "+
		"the commits stay as unreachable objects until git gc prunes them (by default unreachable objects older "+
		"than 2 weeks may be pruned by the next gc), so recover promptly.", shellQuote(name), sha)
}

// run executes the chosen flag. If git refuses -d as not fully merged (its
// view differs from ours, or the repository changed in between) it escalates
// to -D only through the same verification and --force rules, never blindly.
func (d decision) run(ctx context.Context, env *Env) error {
	_, err := env.Git.Run(ctx, d.repo.Dir, "branch", d.flag, "--", d.name)
	if err == nil {
		return nil
	}
	var gerr *gitx.Error
	if d.flag != flagSafe || !errors.As(err, &gerr) || !strings.Contains(gerr.Stderr, notFullyMerged) {
		return fmt.Errorf("delete branch %q: %w", d.name, err)
	}
	if _, ok := d.verifiedWhy(ctx); !ok && !env.Force {
		return skipf("not fully merged; re-run with --force to delete with -D")
	}
	if _, err := env.Git.Run(ctx, d.repo.Dir, "branch", flagForce, "--", d.name); err != nil {
		return fmt.Errorf("delete branch %q: %w", d.name, err)
	}
	return nil
}

// evaluate validates in a fixed order (cheap static checks first, then the
// live repository state) and chooses the flag. All git state is read through
// an uncached handle so that Apply sees the repository as it is now.
func evaluate(ctx context.Context, env *Env, f findings.Finding) (decision, error) {
	if err := checkFinding(f); err != nil {
		return decision{}, err
	}
	repo, err := openRepo(ctx, env, f)
	if err != nil {
		return decision{}, err
	}
	if err := checkRefFormat(ctx, env, repo.Dir, f.Ref); err != nil {
		return decision{}, err
	}
	cfg, err := effectiveConfig(env, f)
	if err != nil {
		return decision{}, err
	}
	d := decision{repo: repo, cfg: cfg, name: f.Ref, verified: f.SuggestedAction.Args["verified"]}
	b, err := d.checkBranch(ctx, f)
	if err != nil {
		return decision{}, err
	}
	if err := d.checkProtection(ctx, b); err != nil {
		return decision{}, err
	}
	if err := d.recheckFlags(ctx, env, f); err != nil {
		return decision{}, err
	}
	return d, d.chooseFlag(ctx, env, b, f)
}

// checkFinding covers the static checks: kind, non-empty ref and a name that
// can never be mistaken for an option or a full ref.
func checkFinding(f findings.Finding) error {
	if f.Kind != findings.KindBranch || f.Ref == "" {
		return skipf("finding is not a branch finding")
	}
	return staticNameCheck(f.Ref)
}

// staticNameCheck refuses names that are dangerous before git is even asked:
// a leading dash (option injection), a refs/ prefix (would address another
// namespace) and revision syntax such as @{-1}.
func staticNameCheck(name string) error {
	switch {
	case strings.HasPrefix(name, "-"):
		return skipf("invalid branch name %q: starts with '-'", name)
	case strings.HasPrefix(name, "refs/"):
		return skipf("invalid branch name %q: must not start with refs/", name)
	case strings.Contains(name, "@{"), name == "@", name == "HEAD":
		return skipf("invalid branch name %q", name)
	case strings.ContainsAny(name, "\x00\n"):
		return skipf("invalid branch name: contains control characters")
	}
	return nil
}

// checkRefFormat asks git whether the name is a valid branch name. The name
// must come back unchanged, since --branch would expand shorthands.
func checkRefFormat(ctx context.Context, env *Env, dir, name string) error {
	out, err := env.Git.Run(ctx, dir, "check-ref-format", "--branch", name)
	if err != nil || out != name {
		return skipf("invalid branch name %q", name)
	}
	return nil
}

// openRepo resolves the repository path through the guard and opens it.
func openRepo(ctx context.Context, env *Env, f findings.Finding) (*gitx.Repo, error) {
	if env.Guard == nil || env.Git == nil {
		return nil, errors.New("delete-branch: no scope guard or git runner configured")
	}
	path, err := env.Guard.Resolve(f.Path)
	if err != nil {
		return nil, skipf("repository outside allowed roots or unresolvable: %v", err)
	}
	repo, err := gitx.Open(ctx, env.Git, path)
	if err != nil {
		return nil, skipf("%s is not a git repository", path)
	}
	return repo, nil
}

// effectiveConfig returns the configuration in effect for the finding.
func effectiveConfig(env *Env, f findings.Finding) (*config.Config, error) {
	if env.Config == nil {
		return nil, errors.New("delete-branch: no configuration")
	}
	cfg, err := env.Config.ForTarget(f.Scope.Path, f.Path)
	if err != nil {
		return nil, skipf("cannot load configuration: %v", err)
	}
	return cfg, nil
}

// checkBranch finds the branch and compares its tip with the scan. A missing
// tip or a mismatch is never overridable: new commits mean the user's work
// changed since the finding was made.
func (d *decision) checkBranch(ctx context.Context, f findings.Finding) (gitx.Branch, error) {
	branches, err := d.repo.ListBranches(ctx)
	if err != nil {
		return gitx.Branch{}, fmt.Errorf("delete-branch: list branches: %w", err)
	}
	for _, b := range branches {
		if b.Name != d.name {
			continue
		}
		want := f.Meta[metaTip]
		switch {
		case want == "":
			return b, skipf("finding has no tip; re-run the scan")
		case b.Tip != want:
			return b, skipf("branch has new commits since the scan")
		}
		d.tip = b.Tip
		return b, nil
	}
	return gitx.Branch{}, skipf("branch no longer exists")
}

// checkProtection refuses checked-out, protected and base branches. None of
// these is overridable by --force.
func (d *decision) checkProtection(ctx context.Context, b gitx.Branch) error {
	if b.WorktreePath != "" {
		return skipf("branch is checked out in %s", b.WorktreePath)
	}
	if gitx.IsProtected(d.cfg.Git.ProtectedBranches, d.name) {
		return skipf("protected branch")
	}
	base, err := d.repo.DefaultBase(ctx, d.cfg.Git.BaseBranches)
	if err != nil && !errors.Is(err, gitx.ErrNoBase) {
		return fmt.Errorf("delete-branch: resolve base branch: %w", err)
	}
	if gitx.IsBaseBranch(base, d.cfg.Git.BaseBranches, d.name) {
		return skipf("base branch")
	}
	return nil
}

// recheckFlags re-queries the risk flags that can change after the scan: an
// open PR (unknown status never blocks) and unpushed commits, the latter only
// where the finding's safety rests on remote containment.
func (d *decision) recheckFlags(ctx context.Context, env *Env, f findings.Finding) error {
	ref := "refs/heads/" + d.name
	if d.cfg.Git.UseGH && !env.Force {
		info := d.repo.OpenPRBranches(ctx, d.repo.Dir, gitx.PROptions{GH: ghRunner})
		if info.HasOpenPR(d.name) {
			return skipf("branch has an open pull request (use --force to override)")
		}
	}
	if !env.Force && relyOnRemote(f) {
		n, err := d.repo.UnpushedCount(ctx, ref)
		if err != nil {
			return skipf("cannot check for unpushed commits: %v", err)
		}
		if n > 0 {
			return skipf("%d commits exist on no remote (use --force to override)", n)
		}
	}
	return nil
}

// relyOnRemote reports whether the finding's safety depends on the commits
// being on a remote: in-remote verification and every stale-branch finding.
func relyOnRemote(f findings.Finding) bool {
	return f.SuggestedAction.Args["verified"] == verifiedInRemote || f.Detector == "stale-branch"
}

// chooseFlag picks -d when git would accept it, else -D only with a fact that
// is re-verified now, else -D under --force, else a skip with the hint.
func (d *decision) chooseFlag(ctx context.Context, env *Env, b gitx.Branch, f findings.Finding) error {
	if d.gitAccepts(ctx, b) {
		d.flag, d.why = flagSafe, "fully merged"
		return nil
	}
	d.flag = flagForce
	if why, ok := d.verifiedWhy(ctx); ok {
		d.why = why
		return nil
	}
	if env.Force {
		d.why = "forced; not verified as merged"
		return nil
	}
	return skipf("not fully merged; re-run with --force to delete with -D")
}

// gitAccepts predicts git's own merge check for -d: the tip must be reachable
// from the upstream when one exists, otherwise from the current HEAD of the
// repository directory.
func (d *decision) gitAccepts(ctx context.Context, b gitx.Branch) bool {
	target := "HEAD"
	if b.Upstream != "" && !b.UpstreamGone {
		target = "refs/remotes/" + b.Upstream
	}
	ok, err := d.repo.IsAncestor(ctx, d.tip, target)
	return err == nil && ok
}

// verifiedWhy re-verifies, right now, one of the facts that justify -D: the
// tip is an ancestor of the resolved base (for any finding), or, only for
// findings that claimed it, a squash/rebase merge is still detected or all
// commits are still contained in remotes. Errors mean unknown, never verified.
func (d decision) verifiedWhy(ctx context.Context) (string, bool) {
	base, err := d.repo.DefaultBase(ctx, d.cfg.Git.BaseBranches)
	if err == nil {
		if why, ok := d.mergedWhy(ctx, base); ok {
			return why, true
		}
	}
	if d.verified == verifiedInRemote {
		ok, err := d.repo.ContainedInRemotes(ctx, "refs/heads/"+d.name)
		if err == nil && ok {
			return "all commits contained in remote-tracking branches, re-verified", true
		}
	}
	return "", false
}

// mergedWhy checks ancestry of the tip in the base and, for a finding that
// claimed a squash merge, the patch-id based squash/rebase detection.
func (d decision) mergedWhy(ctx context.Context, base gitx.Base) (string, bool) {
	if ok, err := d.repo.IsAncestor(ctx, d.tip, base.Ref); err == nil && ok {
		return "merged into " + base.Ref, true
	}
	if d.verified != verifiedSquash {
		return "", false
	}
	res, err := d.repo.MergedInto(ctx, base.Ref, "refs/heads/"+d.name, true)
	if err != nil || !res.Merged {
		return "", false
	}
	return res.Method + "-merged into " + base.Ref + ", re-verified", true
}

// Undo recreates the branch at the recorded tip. It never overwrites: an
// existing branch at the same commit counts as done, one elsewhere is an
// error, and a garbage-collected commit cannot be restored.
func (deleteBranch) Undo(ctx context.Context, env *Env, e session.Entry) error {
	name, sha := e.Undo["branch"], e.Undo["sha"]
	if err := staticNameCheck(name); err != nil || name == "" {
		return fmt.Errorf("undo delete-branch: invalid branch name %q in manifest", name)
	}
	if !commitSHA.MatchString(sha) {
		return fmt.Errorf("undo delete-branch: invalid commit %q in manifest", sha)
	}
	if env.Guard == nil || env.Git == nil {
		return errors.New("undo delete-branch: no scope guard or git runner configured")
	}
	path, err := env.Guard.Resolve(e.Path)
	if err != nil {
		return fmt.Errorf("undo delete-branch: repository %s: %w", e.Path, err)
	}
	repo, err := gitx.Open(ctx, env.Git, path)
	if err != nil {
		return fmt.Errorf("undo delete-branch: %s is not a git repository: %w", path, err)
	}
	// The manifest path may be any directory inside a scan root, while git
	// operates on the enclosing repository, which can lie outside every
	// allowed root (a forged path below a checkout in a scan root nested in
	// a larger repo). Writing a ref there is refused.
	if _, err := env.Guard.Resolve(repo.Dir); err != nil {
		return fmt.Errorf("undo delete-branch: repository %s lies outside the allowed roots: %w", repo.Dir, err)
	}
	if err := checkRefFormat(ctx, env, repo.Dir, name); err != nil {
		return fmt.Errorf("undo delete-branch: %w", err)
	}
	return restoreBranch(ctx, env, repo.Dir, name, sha)
}

// restoreBranch creates the branch once the name is known to be free.
func restoreBranch(ctx context.Context, env *Env, dir, name, sha string) error {
	full, err := env.Git.Run(ctx, dir, "rev-parse", "--verify", "--quiet", sha+"^{commit}")
	if err != nil {
		return fmt.Errorf("commit %s no longer exists (garbage-collected); cannot restore", sha)
	}
	cur, err := env.Git.Run(ctx, dir, "rev-parse", "--verify", "--quiet", "refs/heads/"+name+"^{commit}")
	if err == nil {
		if cur == full {
			return nil
		}
		return &undoConflictError{fmt.Sprintf("branch %s already exists at %s", name, cur)}
	}
	var gerr *gitx.Error
	if !errors.As(err, &gerr) || gerr.ExitCode != 1 {
		return fmt.Errorf("undo delete-branch: look up branch %s: %w", name, err)
	}
	if _, err := env.Git.Run(ctx, dir, "branch", "--", name, full); err != nil {
		return fmt.Errorf("undo delete-branch: recreate %s: %w", name, err)
	}
	return nil
}
