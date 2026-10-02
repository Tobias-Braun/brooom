package action

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/gitx"
	"github.com/Tobias-Braun/brooom/internal/scope"
	"github.com/Tobias-Braun/brooom/internal/session"
	"github.com/Tobias-Braun/brooom/internal/trash"
	"github.com/Tobias-Braun/brooom/internal/walk"
)

// Keys shared between the worktrees detector (finding Meta) and the manifest
// entries the worktree actions write (Entry.Undo). The detector sets "repo"
// and "head". "repo" is repeated in the undo data because the worktree
// directory is gone after the removal and git has to run in the main
// worktree.
const (
	metaRepo = "repo"
	metaHead = "head"

	metaMtimeSource   = "mtime_source"
	mtimeSourceCommit = "commit"

	undoRepo   = "repo"
	undoWT     = "worktree"
	undoBranch = "branch"
	undoHead   = "head"
)

const errOutsideScope = "outside the allowed scope"

func init() {
	Register(removeWorktree{})
	Register(pruneWorktrees{})
}

// openWorktreeRepo resolves the repository named by Meta["repo"] (the main worktree;
// the finding Path is the linked worktree) through the guard and opens it
// without memoization: actions must see the state of the moment, never a
// scan-time cache. Git only ever runs in the returned resolved directory.
func openWorktreeRepo(ctx context.Context, env *Env, f findings.Finding) (*gitx.Repo, error) {
	if env.Guard == nil {
		return nil, errors.New("worktree: no scope guard configured")
	}
	if env.Git == nil {
		return nil, errors.New("worktree: no git runner configured")
	}
	dir := f.Meta[metaRepo]
	if dir == "" {
		return nil, skipf("finding has no repository (meta %q)", metaRepo)
	}
	return openRepoDir(ctx, env, dir)
}

// openRepoDir is openWorktreeRepo for a directory that is already known, which is
// what undo has (the repository comes from the manifest, not a finding).
func openRepoDir(ctx context.Context, env *Env, dir string) (*gitx.Repo, error) {
	resolved, err := env.Guard.ResolveRepoMeta(dir)
	switch {
	case errors.Is(err, scope.ErrOutsideScope):
		return nil, skipf("%s", errOutsideScope)
	case err != nil:
		return nil, skipf("cannot resolve repository %s: %v", dir, err)
	}
	repo, err := gitx.OpenAnchor(ctx, env.Git, resolved)
	switch {
	case errors.Is(err, gitx.ErrUnsafeRepo):
		return nil, skipf("skipped: %v", err)
	case errors.Is(err, gitx.ErrNotRepo), errors.Is(err, gitx.ErrBareRepo):
		return nil, skipf("%s is not a usable git repository any more", resolved)
	case err != nil:
		return nil, fmt.Errorf("worktree: open repository %s: %w", resolved, err)
	}
	return repo, nil
}

// findWorktree returns the entry for path. The comparison is lexical
// (gitx.SamePath): case-insensitive where the filesystem is, and it works for
// directories that no longer exist.
func findWorktree(list []gitx.Worktree, path string) (gitx.Worktree, bool) {
	for _, w := range list {
		if gitx.SamePath(w.Path, path) {
			return w, true
		}
	}
	return gitx.Worktree{}, false
}

// removeWorktree removes a leftover linked worktree.
//
// The directory always goes to the OS trash, so files git ignores (.env,
// agent settings, logs, build output) can be brought back by undo; `git worktree remove` would delete
// them permanently because git treats ignored files as disposable. A nested
// git repository anywhere below the worktree refuses the removal, as it does
// for the trash action. Afterwards only this worktree's registration is
// dropped. A dirty worktree additionally needs Brooom's --force. The delete
// strategy never removes a worktree holding uncommitted or ignored files; a
// worktree with neither is removed with plain `git worktree remove`, which
// loses nothing. A locked worktree is never touched.
type removeWorktree struct{}

// Type implements Action.
func (removeWorktree) Type() findings.ActionType { return findings.ActionRemoveWorktree }

// removeEval is the outcome of re-validating a remove-worktree finding.
type removeEval struct {
	repo  *gitx.Repo
	wt    gitx.Worktree
	path  string
	dirty bool
	// uncommitted counts the entries git status reports (dirty is > 0).
	uncommitted int
	// ignored lists what git ignores in the worktree (see gitx.IgnoredEntries).
	ignored []string
	// trasher moves the directory; nil only for a worktree that holds nothing
	// to lose under the delete strategy, which git removes itself.
	trasher trash.Trasher
	// note says why the open-file check could not vouch for the worktree
	// (unavailable, incomplete); empty when it ran completely.
	note string
}

// evaluateRemove is shared by Plan and Apply so both re-validate everything
// from the live repository state. Order: scope, registration, main/bare,
// lock, existence, drift since the scan, in-use (current directory, open
// files), static path refusals, nested repositories, dirtiness and ignored
// content, risk flags. The lock, in-use, path, nested repository and
// permanent-deletion refusals ignore --force.
func evaluateRemove(ctx context.Context, env *Env, f findings.Finding) (*removeEval, error) {
	repo, err := openWorktreeRepo(ctx, env, f)
	if err != nil {
		return nil, err
	}
	path, err := env.Guard.Resolve(f.Path)
	if err != nil {
		return nil, skipf("%s", errOutsideScope)
	}
	list, err := repo.ListWorktrees(ctx)
	if err != nil {
		return nil, fmt.Errorf("worktree: list worktrees of %s: %w", repo.Dir, err)
	}
	wt, ok := findWorktree(list, path)
	if !ok {
		return nil, skipf("no longer a registered worktree")
	}
	if err := checkRemovable(wt, f); err != nil {
		return nil, err
	}
	if err := checkStillSafe(ctx, env, repo, wt, path, f); err != nil {
		return nil, err
	}
	ev := &removeEval{repo: repo, wt: wt, path: path}
	if err := ev.inspect(ctx, env); err != nil {
		return nil, err
	}
	if err := ev.checkDirty(env); err != nil {
		return nil, err
	}
	if !findings.Actionable(f.RiskFlags, env.Force) {
		return nil, skipf("%s", blockedReason(f.RiskFlags, env.Force))
	}
	return ev, nil
}

// checkStillSafe re-verifies what the scan established and the world may have
// changed since: a detached HEAD that a remote branch held at scan time can
// be unreferenced now (the removal deletes HEAD and its reflog), and files
// may have been edited. Neither is overridable by --force.
func checkStillSafe(ctx context.Context, env *Env, repo *gitx.Repo, wt gitx.Worktree, path string, f findings.Finding) error {
	if err := checkDetachedRemovable(ctx, env, repo, wt, f); err != nil {
		return err
	}
	return checkUnmodified(ctx, path, f)
}

// checkUnmodified refuses when any file below the worktree is newer than the
// LastModified the scan recorded. git status ignores gitignored files, so an
// edit of build output or an env file after the scan (or one a stale cache hid
// during it) is visible only through mtimes, read here with a Fresh walk. A
// finding without LastModified has no baseline and is not checked; a walk that
// fails is refused, because unknown must never read as unchanged. Not
// overridable by --force: a rescan is the way forward. A baseline the
// detector took from the HEAD commit time (Meta "mtime_source" = "commit",
// used when no file mtime was available) is no mtime baseline at all: the
// files are always newer than the commit, so comparing would refuse every
// such worktree. Findings without the key (older versions, report files) are
// compared, which fails safe.
func checkUnmodified(ctx context.Context, path string, f findings.Finding) error {
	if f.LastModified == nil || f.Meta[metaMtimeSource] == mtimeSourceCommit {
		return nil
	}
	sum, err := walk.DirSize(ctx, path, walk.Options{Fresh: true})
	if err != nil {
		return skipf("cannot verify that %s is unmodified since the scan: %v", path, err)
	}
	if sum.NewestModTime.After(*f.LastModified) {
		return skipf("files in the worktree were modified since the scan (newest %s); rescan first",
			sum.NewestModTime.UTC().Format(time.RFC3339))
	}
	return nil
}

// checkRemovable covers the refusals that depend on git's list entry only.
func checkRemovable(wt gitx.Worktree, f findings.Finding) error {
	switch {
	case wt.Main || wt.Bare:
		return skipf("is the main or a bare worktree")
	case wt.Locked:
		msg := string(findings.RiskWorktreeLocked) + ": worktree is locked"
		if wt.LockReason != "" {
			msg += " (" + wt.LockReason + ")"
		}
		return skipf("%s; unlock it with git worktree unlock first", msg)
	case wt.Operation != "":
		return skipf("%s: a %s is in progress in the worktree; finish or abort it first (removal would destroy its state)",
			findings.RiskWorktreeOperation, wt.Operation)
	case wt.HasSubmodules:
		return skipf("worktree has initialized submodules, which git worktree remove refuses; " +
			"deinitialize them (git submodule deinit --all) and remove the worktree manually")
	case wt.DirMissing:
		return skipf("directory is missing; prune-worktrees handles missing directories")
	case f.Meta[metaHead] == "" || wt.Head != f.Meta[metaHead]:
		return skipf("worktree changed since the scan (HEAD moved)")
	case wt.Branch != f.Ref:
		return skipf("worktree changed since the scan (branch is %s, expected %s)", refLabel(wt.Branch), refLabel(f.Ref))
	}
	return nil
}

// checkWorktreeInUse refuses a worktree the caller stands in or that a
// process holds open, before any dirty/force handling and regardless of
// --force: moving it would pull the directory from under a live process.
// The own working directory is checked explicitly (gitx.CwdWithin) because it
// is known on every OS, also where the open-file scan is unavailable or, as
// on Windows, does not report working directories. The returned note is
// non-empty when the open-file check was unavailable or incomplete.
func checkWorktreeInUse(ctx context.Context, path string) (string, error) {
	if gitx.CwdWithin(path) {
		return "", skipf("%s: the current directory is inside the worktree; change directory first", findings.RiskFileOpen)
	}
	return checkOpen(ctx, path)
}

// noteSuffix appends an open-check note to a step description.
func noteSuffix(note string) string {
	if note == "" {
		return ""
	}
	return " (" + note + ")"
}

func refLabel(branch string) string {
	if branch == "" {
		return "detached"
	}
	return branch
}

// inspect runs the trash action's static path refusals (minus the repository
// root one, a worktree root is one by design), refuses nested repositories
// and reads the uncommitted and ignored content of the live directory.
func (ev *removeEval) inspect(ctx context.Context, env *Env) error {
	var err error
	// In-use is checked first and is not overridable by --force.
	if ev.note, err = checkWorktreeInUse(ctx, ev.path); err != nil {
		return err
	}
	if err := refusePath(env, ev.path, false); err != nil {
		return err
	}
	m, err := measureWorktree(ctx, ev.path)
	switch {
	case isGone(err):
		return skipf("directory is missing; prune-worktrees handles missing directories")
	case err != nil:
		return skipf("cannot inspect %s: %v", ev.path, err)
	case m.nestedVCS != "":
		return skipf("%s", m.nestedWhy)
	}
	if ev.uncommitted, err = ev.repo.UncommittedEntries(ctx, ev.path); err != nil {
		return fmt.Errorf("worktree: check %s for uncommitted changes: %w", ev.path, err)
	}
	ev.dirty = ev.uncommitted > 0
	if ev.ignored, err = ev.repo.IgnoredEntries(ctx, ev.path); err != nil {
		return fmt.Errorf("worktree: list ignored files of %s: %w", ev.path, err)
	}
	return nil
}

// checkDirty applies the trasher rules: without --force a dirty worktree is
// skipped.
func (ev *removeEval) checkDirty(env *Env) error {
	if ev.dirty && !env.Force {
		return skipf("worktree has uncommitted changes; decide with `brooom review` to trash it")
	}
	tr, err := trasherOf(env)
	if err != nil {
		return err
	}
	ev.trasher = tr
	return nil
}

// ignoredSummary names the first few ignored entries for messages and states
// the total, so the user sees how much goes to the trash.
func ignoredSummary(entries []string) string {
	const show = 3
	if len(entries) <= show {
		return strings.Join(entries, ", ")
	}
	return fmt.Sprintf("%s and %d more (%d in total)", strings.Join(entries[:show], ", "), len(entries)-show, len(entries))
}

// Plan implements Action.
func (removeWorktree) Plan(ctx context.Context, env *Env, f findings.Finding) (Step, error) {
	ev, err := evaluateRemove(ctx, env, f)
	if err != nil {
		return Step{}, err
	}
	fresh := f
	fresh.Path = ev.path
	return Step{
		Finding:     fresh,
		Description: describe(ev.path, ev.notes()) + noteSuffix(ev.note),
		Command:     chainCommands(displayCommand(ev.path), "git worktree remove -- "+displayQuote(ev.path)),
	}, nil
}

// notes flags what git's own removal would have lost or what needs care.
func (ev *removeEval) notes() []string {
	notes := []string{"worktree"}
	if ev.dirty {
		notes = append(notes, fmt.Sprintf("%d uncommitted entries", ev.uncommitted))
	}
	if len(ev.ignored) > 0 {
		notes = append(notes, "ignored files: "+ignoredSummary(ev.ignored))
	}
	return notes
}

// Apply implements Action. It re-validates first; a step that no longer
// qualifies becomes a skipped entry so the executor reports it as a skip.
func (removeWorktree) Apply(ctx context.Context, env *Env, s Step) (session.Entry, error) {
	f := s.Finding
	en := session.Entry{
		FindingID: f.ID, Detector: f.Detector, Action: findings.ActionRemoveWorktree,
		Path: f.Path, Ref: f.Ref, SizeBytes: f.SizeBytes, At: trashNow().UTC(),
	}
	ev, err := evaluateRemove(ctx, env, f)
	if errors.Is(err, ErrSkipped) {
		en.Status, en.Error = session.StatusSkipped, skipReason(err)
		return en, nil
	}
	if err != nil {
		return failedTrash(en, err)
	}
	en.Path = ev.path
	en.Undo = map[string]string{
		undoRepo: ev.repo.Dir, undoWT: ev.path, undoBranch: ev.wt.Branch, undoHead: ev.wt.Head,
	}
	return ev.applyTrashed(ctx, env, en)
}

// applyTrashed moves the worktree directory to the trash and then drops its
// now dangling registration with `git worktree remove` on the missing path,
// which touches this one entry only (unlike `git worktree prune`), so the
// branch is free again. When that fails the directory is already safe in the
// trash, so the entry keeps the record and stays restorable; only its status
// is failed.
func (ev *removeEval) applyTrashed(ctx context.Context, env *Env, en session.Entry) (session.Entry, error) {
	rec, err := ev.trasher.Remove(ctx, ev.path)
	if err != nil {
		return removeFailed(en, ev.path, rec, err)
	}
	en.Trash = &rec
	en.Restorable = rec.Restorable
	en.SizeBytes = rec.SizeBytes
	en.RecoveryHint = trashedRecoveryHint(en.Undo)
	if ev.dirty {
		en.RecoveryHint += "; the staged/unstaged split of the uncommitted work is not restored, all changes reappear as unstaged"
	}
	if err := ev.deregister(ctx, env); err != nil {
		return failedTrash(en, fmt.Errorf("worktree moved to %s but dropping its registration failed: %w", rec.StoredPath, err))
	}
	en.Status = session.StatusApplied
	return en, nil
}

// deregister drops the registration of the (now missing) worktree directory.
func (ev *removeEval) deregister(ctx context.Context, env *Env) error {
	return deregisterMissing(ctx, env, ev.repo, ev.path, false)
}

// deregisterMissing drops the registration of the worktree at path, whose
// directory is gone. `git worktree remove` on a missing path is what Brooom
// relies on (with force, which git needs for a missing directory in some
// releases, but never the second --force that would lift a lock), but git
// only learned to accept it in later releases than MinGitVersion, so a git
// failure is not final: the fallback deletes the one administrative directory
// <common>/worktrees/<id> whose gitdir file names this worktree. It refuses
// when the entry is locked, cannot be found or is still listed afterwards, and
// never touches other registrations. remove-worktree and prune-worktrees share
// it.
//
// The fallback's os.RemoveAll needs no scope.Guard: the directory is not a
// user path but git's own metadata, found only by listing
// <common>/worktrees of a repository that openRepoDir already resolved
// through the guard, and WorktreeAdminDir returns only entries directly below
// it. Known race: the lock check and the RemoveAll are not atomic, so a lock
// taken in between is not honoured. The window is a few microseconds, git's
// own remove has the same one, and the entry can be re-added, so it is
// accepted rather than papered over.
func deregisterMissing(ctx context.Context, env *Env, repo *gitx.Repo, path string, force bool) error {
	args := []string{"worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	_, gitErr := env.Git.Run(ctx, repo.Dir, append(args, "--", path)...)
	if gitErr == nil {
		return nil
	}
	admin, ok := gitx.WorktreeAdminDir(repo.Common, path)
	if !ok {
		return fmt.Errorf("git worktree remove %s: %w (no administrative directory found for a fallback)", path, gitErr)
	}
	if _, err := os.Lstat(filepath.Join(admin, "locked")); err == nil {
		return fmt.Errorf("git worktree remove %s: %w (and the registration is locked)", path, gitErr)
	}
	if err := os.RemoveAll(admin); err != nil {
		return fmt.Errorf("git worktree remove %s: %w (fallback failed: %w)", path, gitErr, err)
	}
	// A fresh handle: the one in repo may cache its worktree list.
	fresh, err := gitx.OpenAnchor(ctx, env.Git, repo.Dir)
	if err != nil {
		return fmt.Errorf("worktree: reopen %s after the fallback: %w", repo.Dir, err)
	}
	list, err := fresh.ListWorktrees(ctx)
	if err != nil {
		return fmt.Errorf("worktree: list worktrees of %s after the fallback: %w", repo.Dir, err)
	}
	if _, still := findWorktree(list, path); still {
		return fmt.Errorf("worktree: %s is still registered after the fallback", path)
	}
	return nil
}

// restoredPlaceholder stands for the temporary directory the user restores the
// trashed worktree to in the manual recovery hint.
const restoredPlaceholder = "<restored>"

// recoveryStep is one step of the manual recovery of a trashed worktree:
// either a git invocation (git, run through git -C dir) or, when git is nil,
// the prose instruction to move the files back.
type recoveryStep struct {
	dir  string
	git  []string
	text string
}

// trashedRecoverySteps is the sequence that brings a trashed worktree back by
// hand when brooom undo cannot run (for example macOS denying access to
// ~/.Trash). Restoring the directory to its old path and running
// `git worktree add` fails ("already exists"), because deregistering removed
// the administrative directory but the restored directory always holds at
// least a .git file, and `git worktree repair` cannot recreate a missing
// administrative directory. It mirrors undoTrashed instead: add the worktree
// without checkout at a free path, move the restored files over it while
// keeping the new .git file, repair the links and rebuild the index (which
// drops the staged/unstaged split).
func trashedRecoverySteps(undo map[string]string) []recoveryStep {
	path, repo := undo[undoWT], undo[undoRepo]
	add := []string{"worktree", "add", "--no-checkout", "--"}
	if b := undo[undoBranch]; b != "" {
		add = append(add, path, b)
	} else {
		add = []string{"worktree", "add", "--no-checkout", "--detach", "--", path, undo[undoHead]}
	}
	return []recoveryStep{
		{dir: repo, git: add},
		{text: "move everything from " + restoredPlaceholder + " into " + findings.Quote(path) + " except its .git file (keep the new one)"},
		{dir: repo, git: []string{"worktree", "repair", path}},
		{dir: path, git: []string{"reset", "-q"}},
	}
}

// trashedRecoveryHint renders trashedRecoverySteps as one line. Every git
// command carries its directory with -C so it works from any current
// directory.
func trashedRecoveryHint(undo map[string]string) string {
	var parts []string
	for _, st := range trashedRecoverySteps(undo) {
		if st.git == nil {
			parts = append(parts, st.text)
			continue
		}
		cmd := "git"
		if st.dir != "" {
			cmd += " -C " + findings.Quote(st.dir)
		}
		for _, a := range st.git {
			cmd += " " + findings.Quote(a)
		}
		parts = append(parts, cmd)
	}
	return "restore the directory from the trash record to a temporary location " + restoredPlaceholder +
		" (brooom undo does all of this; do not restore it to the original path, git worktree add refuses an existing directory), then: " +
		strings.Join(parts, "; ")
}

// readdHint is the manual git command that recreates a worktree whose
// directory is gone for good (a prune of missing metadata). A trashed
// worktree needs trashedRecoveryHint instead.
func readdHint(undo map[string]string) string {
	path := findings.Quote(undo[undoWT])
	if b := undo[undoBranch]; b != "" {
		return "git worktree add " + path + " " + findings.Quote(b)
	}
	return "git worktree add --detach " + path + " " + undo[undoHead]
}

// checkDetachedRemovable re-verifies at apply time that removing a detached
// worktree loses no commit: HEAD must be held by a branch, remote branch or
// tag (checkDetachedHead), or all its commits must be patch-equivalent to
// commits on the base branch, which is what an agent worktree left at a
// pre-rebase commit looks like once its work landed. A commit that is
// genuinely unique, or a check that cannot answer, refuses, and --force does
// not lift that.
func checkDetachedRemovable(ctx context.Context, env *Env, repo *gitx.Repo, wt gitx.Worktree, f findings.Finding) error {
	err := checkDetachedHead(ctx, env, repo, wt)
	if err == nil || !errors.Is(err, ErrSkipped) {
		return err
	}
	if patchEquivalentToBase(ctx, env, repo, wt.Head, f) {
		return nil
	}
	return err
}

// patchEquivalentToBase reports whether every commit reachable from head but
// not from the base branch has an equal patch on the base (see
// gitx.Repo.MergedInto). Any error or unknown base counts as no.
func patchEquivalentToBase(ctx context.Context, env *Env, repo *gitx.Repo, head string, f findings.Finding) bool {
	cfg, err := effectiveConfig(env, f)
	if err != nil {
		return false
	}
	base, err := repo.DefaultBase(ctx, cfg.Git.BaseBranches)
	if err != nil {
		return false
	}
	res, err := repo.MergedInto(ctx, base.FullRef, head, cfg.Detectors.MergedBranch.Mode == config.MergeAncestorSquash)
	return err == nil && res.Merged
}
