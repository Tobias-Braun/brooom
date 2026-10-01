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
	metaTip   = "tip"
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
	// path is the finding's repository path, the key of Env.plannedDeletes.
	path string
	tip  string
	flag string
	// why says what justified the flag, for the step description.
	why string
	// merge caches mergedFact: the answer is needed twice per evaluation and
	// the squash detection is not cheap.
	merge *mergeFact
}

// mergeFact is the live answer to "is the tip merged into the base branch".
type mergeFact struct {
	why string
	ok  bool
	// heuristic is true when only the patch-id detection (squash or rebase)
	// found the merge; ancestry is a fact, patch-id equality is a guess.
	heuristic bool
	// unpushed is true when the merge exists only in a local base that no
	// remote has (see gitx.Base.Unpushed); such a merge proves nothing about
	// the remote, so it needs the same remote containment as a heuristic one.
	unpushed bool
}

// command is the exact git invocation for display.
func (d decision) command() string {
	return "git branch " + d.flag + " -- " + findings.Quote(d.name)
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
	step := Step{Finding: f, Description: desc, Command: d.command()}
	if runFactsFrom(ctx) != nil {
		step.live = d
	}
	return step, nil
}

// Apply evaluates again, because the repository may have changed since Plan,
// and runs git branch. In the apply pass the executor re-plans every item
// immediately before applying it, so that decision is used instead of a
// second identical evaluation; the deletion itself is a compare-and-swap on
// the evaluated tip either way. A skip at this point is reported as a skipped
// entry.
func (deleteBranch) Apply(ctx context.Context, env *Env, s Step) (session.Entry, error) {
	f := s.Finding
	en := session.Entry{
		FindingID: f.ID, Detector: f.Detector, Action: findings.ActionDeleteBranch,
		Path: f.Path, Ref: f.Ref, At: time.Now().UTC(),
	}
	d, err := s.branchDecision(ctx, env)
	if errors.Is(err, ErrSkipped) {
		en.Status, en.Error = session.StatusSkipped, skipReason(err)
		return en, nil
	}
	if err != nil {
		return failedBranch(en, err)
	}
	en.Path = d.repo.Dir
	// Git drops branch.<name>.* together with the branch, so the tracking
	// configuration has to be read before deleting.
	// The read happens between evaluate and run, so a concurrent config
	// change can make it stale; undo validates it again and a wrong value
	// only affects which tracking config is restored.
	upstream := d.readUpstream(ctx, env)
	sha, err := d.run(ctx, env)
	if err != nil {
		if errors.Is(err, ErrSkipped) {
			en.Status, en.Error = session.StatusSkipped, skipReason(err)
			return en, nil
		}
		return failedBranch(en, err)
	}
	en.Status = session.StatusApplied
	en.Undo = map[string]string{"branch": d.name, "sha": sha}
	for k, v := range upstream {
		en.Undo[k] = v
	}
	en.Restorable = true
	en.RecoveryHint = branchRecoveryHint(d.name, sha, d.reachableFrom(ctx, env, sha))
	return en, nil
}

// branchDecision returns the decision the apply-pass re-plan attached to s,
// or a fresh evaluation when there is none (a step from the pre-confirmation
// plan, or Apply called on its own).
func (s Step) branchDecision(ctx context.Context, env *Env) (decision, error) {
	if d, ok := s.live.(decision); ok {
		return d, nil
	}
	return evaluate(ctx, env, s.Finding)
}

// reachableFrom names a branch, remote-tracking branch or tag that still
// holds the deleted tip, or "" when none does or git cannot say (unknown is
// treated as unreachable, so the warning is only ever dropped on evidence).
// Local branches that this run is going to delete as well (Env.plannedDeletes)
// do not count: x2 built on x1 keeps x1's commits only until x2 is deleted
// too, and a hint saying otherwise would mislead. Stash refs are not
// consulted: a stash descends from a commit without being a reason to consider
// it kept.
func (d decision) reachableFrom(ctx context.Context, env *Env, sha string) string {
	out, err := env.Git.Run(ctx, d.repo.Dir, "for-each-ref", "--contains", sha,
		"--format=%(refname)%09%(refname:short)", "refs/heads", "refs/remotes", "refs/tags")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(out, "\n") {
		full, short, ok := strings.Cut(strings.TrimSpace(line), "\t")
		if !ok {
			continue
		}
		if _, planned := env.plannedDeletes[plannedKey(d.path, full)]; !planned {
			return short
		}
	}
	return ""
}

func failedBranch(en session.Entry, err error) (session.Entry, error) {
	en.Status, en.Error = session.StatusFailed, err.Error()
	return en, err
}

// branchRecoveryHint is accurate about git: deleting a branch also deletes its
// own reflog. When another ref (keptBy) still holds the tip, the commits stay
// reachable and gc never prunes them, so the hint says that instead of
// warning about unreachable objects; only when nothing holds them (a forced
// deletion of unmerged work) are they unreachable objects that a later gc
// prunes.
func branchRecoveryHint(name, sha, keptBy string) string {
	if keptBy != "" {
		return fmt.Sprintf("run inside the repository: git branch %s %s. The commits are still reachable from %s, "+
			"so git gc will not prune them.", findings.Quote(name), sha, findings.Quote(keptBy))
	}
	return fmt.Sprintf("run inside the repository: git branch %s %s. Deleting a branch also deletes its reflog; "+
		"the commits stay as unreachable objects until git gc prunes them (by default unreachable objects older "+
		"than 2 weeks may be pruned by the next gc), so recover promptly.", findings.Quote(name), sha)
}

// run executes the chosen flag and returns the sha of the commit that was
// actually deleted, which is what undo must restore. If git refuses -d as not
// fully merged (its view differs from ours, or the repository changed in
// between) it escalates to -D only through the same verification and --force
// rules, never blindly.
func (d *decision) run(ctx context.Context, env *Env) (string, error) {
	if d.flag == flagForce {
		return d.deleteForced(ctx, env)
	}
	out, err := env.Git.Run(ctx, d.repo.Dir, "branch", d.flag, "--", d.name)
	if err == nil {
		// git ran its own merge check against the tip it deleted; its report
		// is the truth even when the branch moved after our re-validation.
		return d.expandDeleted(ctx, env, deletedSHA(out)), nil
	}
	var gerr *gitx.Error
	if !errors.As(err, &gerr) || !strings.Contains(gerr.Stderr, notFullyMerged) {
		return "", fmt.Errorf("delete branch %q: %w", d.name, err)
	}
	if _, ok := d.verifiedWhy(ctx); !ok && !env.Force {
		return "", skipf("not fully merged; re-run with --force to delete with -D")
	}
	return d.deleteForced(ctx, env)
}

// deletedSHAPattern matches the "(was <sha>)" part of git's "Deleted branch"
// message.
var deletedSHAPattern = regexp.MustCompile(`\(was ([0-9a-fA-F]{7,64})\)`)

// deletedSHA extracts the commit git reports as deleted, or "" if the output
// has none.
func deletedSHA(out string) string {
	m := deletedSHAPattern.FindStringSubmatch(out)
	if m == nil {
		return ""
	}
	return m[1]
}

// expandDeleted turns the abbreviated sha git printed into the full one (the
// commit still exists as an unreachable object). Without a usable report the
// re-validated tip is the best knowledge; an abbreviation is kept if it cannot
// be expanded, since undo accepts it.
func (d decision) expandDeleted(ctx context.Context, env *Env, short string) string {
	if short == "" {
		return d.tip
	}
	full, err := env.Git.Run(ctx, d.repo.Dir, "rev-parse", "--verify", "--quiet", short+"^{commit}")
	if err != nil {
		return short
	}
	return full
}

// deleteForced deletes the branch ref only if it still points to the verified
// tip (compare-and-swap through `git update-ref -d <ref> <old>`). `git branch
// -D` deletes whatever the ref points to at that moment, so a commit made or a
// fetch landed after re-validation (the merge check above ran on the old tip)
// would be destroyed. A moved ref is a skip, not a failure. update-ref does not
// know about checked-out branches, so that check is repeated right before; a
// worktree created in the short window between that check and update-ref is
// not caught (best effort, documented in ARCHITECTURE.md).
func (d decision) deleteForced(ctx context.Context, env *Env) (string, error) {
	ref := "refs/heads/" + d.name
	if err := d.checkNotCheckedOut(ctx, ref); err != nil {
		return "", err
	}
	if _, err := env.Git.Run(ctx, d.repo.Dir, "update-ref", "-d", ref, d.tip); err != nil {
		cur, rerr := env.Git.Run(ctx, d.repo.Dir, "rev-parse", "--verify", "--quiet", ref)
		if rerr != nil || cur != d.tip {
			return "", skipf("branch moved during apply")
		}
		return "", fmt.Errorf("delete branch %q: %w", d.name, err)
	}
	// update-ref leaves branch.<name>.* behind, which git branch -D removes;
	// a stale section would attach to an unrelated branch of the same name.
	// It is best effort: the branch itself is already gone.
	_, _ = env.Git.Run(ctx, d.repo.Dir, "config", "--local", "--remove-section", "branch."+d.name)
	return d.tip, nil
}

// checkNotCheckedOut refuses a ref that a worktree has checked out now.
func (d decision) checkNotCheckedOut(ctx context.Context, ref string) error {
	wts, err := d.repo.ListWorktrees(ctx)
	if err != nil {
		return fmt.Errorf("delete branch %q: list worktrees: %w", d.name, err)
	}
	for _, w := range wts {
		if w.BranchRef == ref {
			return skipf("branch is checked out in %s", w.Path)
		}
	}
	return nil
}

// Manifest keys of the tracking configuration recorded on deletion.
const (
	undoUpstreamRemote = "upstream_remote"
	undoUpstreamMerge  = "upstream_merge"
)

// remoteName and mergeRef are the shapes of recorded tracking values. They
// exclude options, URLs, whitespace and control characters; the merge ref is
// additionally checked by git (check-ref-format) on undo.
var (
	remoteName = regexp.MustCompile(`^[A-Za-z0-9_.][A-Za-z0-9_./-]*$`)
	mergeRef   = regexp.MustCompile(`^refs/heads/[A-Za-z0-9_./+@#%=,-]+$`)
)

// validRemote applies the remoteName shape and additionally rejects ".."
// anywhere, so a recorded remote can never climb out of refs/remotes/.
func validRemote(remote string) bool {
	return remoteName.MatchString(remote) && !strings.Contains(remote, "..")
}

// readUpstream returns the branch.<name>.remote/merge configuration to record
// for undo, or nil when the branch tracks nothing or the values are not
// something undo would accept.
func (d decision) readUpstream(ctx context.Context, env *Env) map[string]string {
	get := func(key string) string {
		out, err := env.Git.Run(ctx, d.repo.Dir, "config", "--local", "--get", "branch."+d.name+"."+key)
		if err != nil {
			return ""
		}
		return out
	}
	remote, merge := get("remote"), get("merge")
	if !validRemote(remote) || !mergeRef.MatchString(merge) {
		return nil
	}
	return map[string]string{undoUpstreamRemote: remote, undoUpstreamMerge: merge}
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
	d := decision{repo: repo, cfg: cfg, name: f.Ref, path: f.Path}
	b, err := d.checkBranch(ctx, f)
	if err != nil {
		return decision{}, err
	}
	if err := d.checkProtection(ctx, b); err != nil {
		return decision{}, err
	}
	if err := d.recheckFlags(ctx, env); err != nil {
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

// staticNameCheck skips names that are dangerous before git is even asked. The
// rules live in gitx.RefusedBranchName so the detectors apply the same ones.
func staticNameCheck(name string) error {
	if reason := gitx.RefusedBranchName(name); reason != "" {
		return skipf("%s", reason)
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
	path, err := env.Guard.ResolveRepoMeta(f.Path)
	if err != nil {
		return nil, skipf("repository outside allowed roots or unresolvable: %v", err)
	}
	// A Plan pass shares one memoizing handle per repository; the apply pass
	// opens an uncached one so it sees the repository as it is now, sharing
	// only the run facts.
	var repo *gitx.Repo
	switch snap, facts := planSnapshotFrom(ctx), runFactsFrom(ctx); {
	case snap != nil:
		repo, err = snap.cache.AnchorRepo(ctx, path)
	case facts != nil:
		repo, err = facts.OpenAnchor(ctx, path)
	default:
		repo, err = gitx.OpenAnchor(ctx, env.Git, path)
	}
	if errors.Is(err, gitx.ErrUnsafeRepo) {
		return nil, skipf("skipped: %v", err)
	}
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

// recheckFlags re-queries the risk flag that can change after the scan: an
// open PR (unknown status never blocks).
func (d *decision) recheckFlags(ctx context.Context, env *Env) error {
	if d.cfg.Git.UseGH && !env.Force {
		info := d.repo.OpenPRBranches(ctx, d.repo.Dir, gitx.PROptions{GH: ghRunner})
		if info.HasOpenPR(d.name) {
			return skipf("branch has an open pull request (use --force to override)")
		}
	}
	return nil
}

// chooseFlag picks -d when git would accept it, else -D only with a fact that
// is re-verified now, else -D under --force, else a skip with the hint.
//
// Safety policy (pinned by tests, see ARCHITECTURE.md): every justification
// for -D is re-derived here from the repository, never from the finding. A
// merge found only by the patch-id heuristic (squash, rebase) justifies -D
// solely when every commit of the branch is also on a remote (the same holds
// for a merge into a local base no remote has); a squash-merged
// branch whose commits exist on no remote needs --force, because the heuristic
// can be wrong and a wrong guess would leave the work only as unreachable
// objects. The gate is live (ContainedInRemotes) and not keyed on the detector name
// or the finding's "verified" claim, which a findings file can edit.
func (d *decision) chooseFlag(ctx context.Context, env *Env, b gitx.Branch, f findings.Finding) error {
	if target, ok := d.gitAccepts(ctx, b); ok {
		d.flag, d.why = flagSafe, "fully merged into "+target
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
	return d.refusal(ctx)
}

// refusal is the skip for a branch that no verified fact justifies deleting.
// A branch only the patch-id heuristic (or a merge into an unpushed local base)
// calls merged says so and quotes how many commits would be lost; the sentence uses semicolons only, because the
// count phrase may already carry a parenthesis.
func (d *decision) refusal(ctx context.Context) error {
	m := d.mergedFact(ctx)
	if !m.ok || (!m.heuristic && !m.unpushed) {
		return skipf("not fully merged; re-run with --force to delete with -D")
	}
	found := "the merge is only detected by the patch-id heuristic"
	if !m.heuristic {
		found = "the merge exists only in a local base that no remote has"
	}
	phrase := "its commits are on no remote"
	if n, err := d.repo.UniqueCount(ctx, d.name); err == nil {
		phrase = gitx.OnlyOnBranchPhrase(n)
	}
	return skipf("not fully merged; %s; %s and no remote has the commits; "+
		"re-run with --force to delete with -D", phrase, found)
}

// gitAccepts predicts git's own merge check for -d: the tip must be reachable
// from the upstream when one exists, otherwise from the current HEAD of the
// repository directory. It also returns the reference that justified the
// deletion, in the words shown to the user ("upstream origin/x" or "HEAD"), so
// the plan never claims a merge into something git did not check.
func (d *decision) gitAccepts(ctx context.Context, b gitx.Branch) (string, bool) {
	target, label := "HEAD", "HEAD"
	if b.Upstream != "" && !b.UpstreamGone {
		// UpstreamRef also names a local upstream (remote "."), which lives
		// under refs/heads/ and not under refs/remotes/.
		target, label = b.UpstreamRef, "upstream "+b.Upstream
	}
	ok, err := d.repo.IsAncestor(ctx, d.tip, target)
	return label, err == nil && ok
}

// verifiedWhy re-verifies, right now, one of the facts that justify -D: the
// tip is an ancestor of the resolved base, all commits are contained in
// remotes, or a squash/rebase merge is detected and all commits are contained
// in remotes (the heuristic alone is not enough, see chooseFlag). Nothing is
// taken from the finding. Errors mean unknown, never verified.
func (d *decision) verifiedWhy(ctx context.Context) (string, bool) {
	m := d.mergedFact(ctx)
	if m.ok && !m.heuristic && !m.unpushed {
		return m.why, true
	}
	ok, err := d.repo.ContainedInRemotes(ctx, "refs/heads/"+d.name)
	if err != nil || !ok {
		return "", false
	}
	if m.ok {
		return m.why + "; all commits contained in remote-tracking branches", true
	}
	return "all commits contained in remote-tracking branches, re-verified", true
}

// mergedFact derives from the repository whether the tip is merged into a
// base candidate: by ancestry, or by the patch-id based squash/rebase
// detection. It is computed once per decision.
func (d *decision) mergedFact(ctx context.Context) mergeFact {
	if d.merge == nil {
		d.merge = &mergeFact{}
		if bases, err := d.repo.BaseCandidates(ctx, d.cfg.Git.BaseBranches); err == nil {
			*d.merge = d.mergedWhy(ctx, bases)
		}
	}
	return *d.merge
}

// mergedWhy checks ancestry of the tip in every base candidate, then the
// squash/rebase detection, and words the first match.
func (d *decision) mergedWhy(ctx context.Context, bases []gitx.Base) mergeFact {
	base, res, err := d.repo.MergedIntoAny(ctx, bases, "refs/heads/"+d.name, true)
	if err != nil || !res.Merged {
		return mergeFact{}
	}
	m := mergeFact{ok: true, unpushed: base.Unpushed, heuristic: res.Method != gitx.MethodAncestor}
	if m.heuristic {
		m.why = res.Method + "-merged into " + base.Display() + ", re-verified"
	} else {
		m.why = "merged into " + base.Display()
	}
	return m
}

// Undo recreates the branch at the recorded tip. It never overwrites: an
// existing branch at the same commit counts as done, one elsewhere is an
// error, and a garbage-collected commit cannot be restored.
func (deleteBranch) Undo(ctx context.Context, env *Env, e session.Entry) error {
	name, sha, up, err := checkUndoEntry(e.Undo)
	if err != nil {
		return fmt.Errorf("undo delete-branch: %w", err)
	}
	if env.Guard == nil || env.Git == nil {
		return errors.New("undo delete-branch: no scope guard or git runner configured")
	}
	path, err := env.Guard.ResolveRepoMeta(e.Path)
	if err != nil {
		return fmt.Errorf("undo delete-branch: repository %s: %w", e.Path, err)
	}
	repo, err := gitx.OpenAnchor(ctx, env.Git, path)
	if err != nil {
		return fmt.Errorf("undo delete-branch: %s is not a git repository: %w", path, err)
	}
	// The manifest path may be any directory inside a scan root, while git
	// operates on the enclosing repository, which can lie outside every
	// allowed root (a forged path below a checkout in a scan root nested in
	// a larger repo). Writing a ref there is refused.
	if _, err := env.Guard.ResolveRepoMeta(repo.Dir); err != nil {
		return fmt.Errorf("undo delete-branch: repository %s lies outside the allowed roots: %w", repo.Dir, err)
	}
	if err := checkRefFormat(ctx, env, repo.Dir, name); err != nil {
		return fmt.Errorf("undo delete-branch: %w", err)
	}
	if err := checkMergeRef(ctx, env, repo.Dir, up); err != nil {
		return fmt.Errorf("undo delete-branch: %w", err)
	}
	return restoreBranch(ctx, env, repo.Dir, name, sha, up)
}

// checkUndoEntry statically validates everything undo takes from the manifest,
// which is never trusted, before git is asked anything.
func checkUndoEntry(undo map[string]string) (name, sha string, up upstream, err error) {
	name, sha = undo["branch"], undo["sha"]
	if err := staticNameCheck(name); err != nil || name == "" {
		return "", "", upstream{}, fmt.Errorf("invalid branch name %q in manifest", name)
	}
	if !commitSHA.MatchString(sha) {
		return "", "", upstream{}, fmt.Errorf("invalid commit %q in manifest", sha)
	}
	up, err = upstreamFromEntry(undo)
	return name, sha, up, err
}

// upstream is the tracking configuration of a deleted branch.
type upstream struct{ remote, merge string }

// upstreamFromEntry reads and statically validates the recorded tracking
// configuration; the manifest is not trusted. Entries written before it was
// recorded have none, which is fine, but one key without the other is not.
func upstreamFromEntry(undo map[string]string) (upstream, error) {
	remote, merge := undo[undoUpstreamRemote], undo[undoUpstreamMerge]
	if remote == "" && merge == "" {
		return upstream{}, nil
	}
	if !validRemote(remote) || !mergeRef.MatchString(merge) {
		return upstream{}, fmt.Errorf("invalid upstream %q %q in manifest", remote, merge)
	}
	return upstream{remote, merge}, nil
}

// checkMergeRef lets git judge the recorded merge ref and remote (for example
// ".."), the latter as the refs/remotes/<remote> namespace it is used in.
func checkMergeRef(ctx context.Context, env *Env, dir string, up upstream) error {
	if up == (upstream{}) {
		return nil
	}
	if _, err := env.Git.Run(ctx, dir, "check-ref-format", up.merge); err != nil {
		return fmt.Errorf("invalid upstream ref %q in manifest", up.merge)
	}
	if up.remote != "." {
		if _, err := env.Git.Run(ctx, dir, "check-ref-format", "refs/remotes/"+up.remote+"/x"); err != nil {
			return fmt.Errorf("invalid upstream remote %q in manifest", up.remote)
		}
	}
	return nil
}

// restoreUpstream puts the tracking configuration back on a branch that undo
// just created. `git branch --set-upstream-to` is preferred because git
// validates it, but it needs the remote-tracking ref; without it (remote
// branch deleted meanwhile) the two config keys are written directly, which
// is exactly what the branch had before.
func restoreUpstream(ctx context.Context, env *Env, dir, name string, up upstream) error {
	if up == (upstream{}) {
		return nil
	}
	short := strings.TrimPrefix(up.merge, "refs/heads/")
	if up.remote != "." {
		remoteRef := "refs/remotes/" + up.remote + "/" + short
		if _, err := env.Git.Run(ctx, dir, "rev-parse", "--verify", "--quiet", remoteRef); err == nil {
			if _, err := env.Git.Run(ctx, dir, "branch", "--set-upstream-to="+up.remote+"/"+short, name); err == nil {
				return nil
			}
		}
	}
	for _, kv := range [][2]string{{"remote", up.remote}, {"merge", up.merge}} {
		if _, err := env.Git.Run(ctx, dir, "config", "--local", "branch."+name+"."+kv[0], kv[1]); err != nil {
			return fmt.Errorf("undo delete-branch: %s restored, but its upstream could not be: %w", name, err)
		}
	}
	return nil
}

// restoreBranch creates the branch once the name is known to be free and, only
// then, restores its tracking configuration.
func restoreBranch(ctx context.Context, env *Env, dir, name, sha string, up upstream) error {
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
	if err := checkRefCollision(ctx, env, dir, name, full); err != nil {
		return err
	}
	if _, err := env.Git.Run(ctx, dir, "branch", "--", name, full); err != nil {
		// A branch created between the check above and git branch is the same
		// conflict; only a failure that is no collision stays an error.
		if cerr := checkRefCollision(ctx, env, dir, name, full); cerr != nil {
			return cerr
		}
		return fmt.Errorf("undo delete-branch: recreate %s: %w", name, err)
	}
	return restoreUpstream(ctx, env, dir, name, up)
}

// checkRefCollision reports the file/directory clash of ref names as a
// conflict: git stores refs/heads/<name> as a file, so it cannot coexist with a
// branch named like a proper prefix of it (a/b needs a to be a directory) or
// with branches below it (feat needs feat/ not to exist). Creating the branch
// would fail with git's "cannot lock ref" error, which is not a conflict as far
// as callers can tell. The hint names a free alternative that keeps the commit.
func checkRefCollision(ctx context.Context, env *Env, dir, name, sha string) error {
	hint := fmt.Sprintf("restore it under another name with: git branch %s %s",
		findings.Quote(name+"-restored"), sha)
	parts := strings.Split(name, "/")
	for i := 1; i < len(parts); i++ {
		prefix := strings.Join(parts[:i], "/")
		if _, err := env.Git.Run(ctx, dir, "rev-parse", "--verify", "--quiet", "refs/heads/"+prefix); err == nil {
			return &undoConflictError{fmt.Sprintf("branch %s exists, so %s cannot be created; %s", prefix, name, hint)}
		}
	}
	out, err := env.Git.Run(ctx, dir, "for-each-ref", "--count=1", "--format=%(refname:short)", "refs/heads/"+name+"/")
	if err == nil && strings.TrimSpace(out) != "" {
		return &undoConflictError{fmt.Sprintf("branch %s exists, so %s cannot be created; %s", strings.TrimSpace(out), name, hint)}
	}
	return nil
}
