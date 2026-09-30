package action

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/gitx"
	"github.com/Tobias-Braun/brooom/internal/output"
	"github.com/Tobias-Braun/brooom/internal/scope"
	"github.com/Tobias-Braun/brooom/internal/session"
	"github.com/Tobias-Braun/brooom/internal/trash"
)

// Keys shared between the worktrees detector (finding Meta) and the manifest
// entries the worktree actions write (Entry.Undo). The detector sets "repo"
// and "head". "repo" is repeated in the undo data because the worktree
// directory is gone after the removal and git has to run in the main
// worktree.
const (
	metaRepo   = "repo"
	metaHead   = "head"
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

// openRepo resolves the repository named by Meta["repo"] (the main worktree;
// the finding Path is the linked worktree) through the guard and opens it
// without memoization: actions must see the state of the moment, never a
// scan-time cache. Git only ever runs in the returned resolved directory.
func openRepo(ctx context.Context, env *Env, f findings.Finding) (*gitx.Repo, error) {
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

// openRepoDir is openRepo for a directory that is already known, which is
// what undo has (the repository comes from the manifest, not a finding).
func openRepoDir(ctx context.Context, env *Env, dir string) (*gitx.Repo, error) {
	resolved, err := env.Guard.Resolve(dir)
	switch {
	case errors.Is(err, scope.ErrOutsideScope):
		return nil, skipf("%s", errOutsideScope)
	case err != nil:
		return nil, skipf("cannot resolve repository %s: %v", dir, err)
	}
	repo, err := gitx.Open(ctx, env.Git, resolved)
	switch {
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

// removeWorktree deletes a leftover linked worktree.
//
// A worktree that git considers clean is removed with `git worktree remove`
// (git's own --force is never passed). Files ignored by git, such as build
// output, are deleted together with the directory and undo does not bring
// them back; they are reproducible. A dirty worktree is only touched with
// Brooom's --force, and then only by moving the directory to the trash so
// nothing is lost. A locked worktree is never touched.
type removeWorktree struct{}

// Type implements Action.
func (removeWorktree) Type() findings.ActionType { return findings.ActionRemoveWorktree }

// removeEval is the outcome of re-validating a remove-worktree finding.
type removeEval struct {
	repo  *gitx.Repo
	wt    gitx.Worktree
	path  string
	dirty bool
	// trasher is set only for dirty worktrees.
	trasher trash.Trasher
}

// evaluateRemove is shared by Plan and Apply so both re-validate everything
// from the live repository state. Order: scope, registration, main/bare,
// lock, existence, drift since the scan, dirtiness, risk flags. The lock and
// the permanent-deletion refusals ignore --force.
func evaluateRemove(ctx context.Context, env *Env, f findings.Finding) (*removeEval, error) {
	repo, err := openRepo(ctx, env, f)
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
	ev := &removeEval{repo: repo, wt: wt, path: path}
	if ev.dirty, err = repo.IsDirty(ctx, path); err != nil {
		return nil, fmt.Errorf("worktree: check %s for uncommitted changes: %w", path, err)
	}
	if err := ev.checkDirty(env, f); err != nil {
		return nil, err
	}
	if !findings.Actionable(f.RiskFlags, env.Force) {
		return nil, skipf("%s", blockedReason(f.RiskFlags, env.Force))
	}
	return ev, nil
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
	case wt.DirMissing:
		return skipf("directory is missing; prune-worktrees handles missing directories")
	case f.Meta[metaHead] == "" || wt.Head != f.Meta[metaHead]:
		return skipf("worktree changed since the scan (HEAD moved)")
	case wt.Branch != f.Ref:
		return skipf("worktree changed since the scan (branch is %s, expected %s)", refLabel(wt.Branch), refLabel(f.Ref))
	}
	return nil
}

func refLabel(branch string) string {
	if branch == "" {
		return "detached"
	}
	return branch
}

// checkDirty applies the dirty rules: without --force a dirty worktree is
// skipped; with --force it needs a trasher that is not the delete strategy.
func (ev *removeEval) checkDirty(env *Env, f findings.Finding) error {
	if !ev.dirty {
		return nil
	}
	if !env.Force {
		return skipf("worktree has uncommitted changes; re-run with --force to trash it")
	}
	tr, err := trasherFor(env, f.Detector)
	if err != nil {
		return err
	}
	if tr.Strategy() == config.StrategyDelete {
		return skipf("refusing to permanently delete uncommitted work; use --trash-strategy trash or quarantine")
	}
	ev.trasher = tr
	return nil
}

// Plan implements Action.
func (removeWorktree) Plan(ctx context.Context, env *Env, f findings.Finding) (Step, error) {
	ev, err := evaluateRemove(ctx, env, f)
	if err != nil {
		return Step{}, err
	}
	fresh := f
	fresh.Path = ev.path
	if !ev.dirty {
		return Step{
			Finding: fresh,
			Description: fmt.Sprintf("remove worktree %s (%s) with git worktree remove",
				filepath.Base(ev.path), output.FormatSize(f.SizeBytes)),
			Command: "git worktree remove -- " + shellQuote(ev.path),
		}, nil
	}
	strategy := ev.trasher.Strategy()
	return Step{
		Finding:     fresh,
		Description: describe(strategy, f.SizeBytes, ev.path, []string{"worktree with uncommitted changes"}),
		Command:     displayCommand(strategy, ev.path) + " && git worktree prune",
	}, nil
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
	if ev.dirty {
		return ev.applyTrashed(ctx, env, en)
	}
	return ev.applyClean(ctx, env, en)
}

// applyClean removes a clean worktree with plain `git worktree remove`.
// git's own refusals (submodules, untracked files that appeared since the
// check, a lock taken meanwhile) surface as the failure message.
func (ev *removeEval) applyClean(ctx context.Context, env *Env, en session.Entry) (session.Entry, error) {
	if _, err := env.Git.Run(ctx, ev.repo.Dir, "worktree", "remove", "--", ev.path); err != nil {
		return failedTrash(en, fmt.Errorf("git worktree remove %s: %w", ev.path, err))
	}
	en.Status = session.StatusApplied
	en.Restorable = true
	en.RecoveryHint = readdHint(en.Undo) +
		" (files ignored by git, such as build output, were deleted with the directory and are not restored)"
	return en, nil
}

// applyTrashed moves a dirty worktree to the trash and then prunes its now
// dangling registration so the branch is free again. When the prune fails the
// directory is already safe in the trash, so the entry keeps the record and
// stays restorable; only its status is failed.
func (ev *removeEval) applyTrashed(ctx context.Context, env *Env, en session.Entry) (session.Entry, error) {
	rec, err := ev.trasher.Remove(ctx, ev.path)
	if err != nil {
		return removeFailed(en, ev.path, rec, err)
	}
	en.Trash = &rec
	en.Restorable = rec.Restorable
	en.SizeBytes = rec.SizeBytes
	en.RecoveryHint = "restore the directory from the trash record (brooom undo does it), then " + readdHint(en.Undo) +
		"; the staged/unstaged split of the uncommitted work is not restored, all changes reappear as unstaged"
	if !rec.Restorable {
		en.RecoveryHint = "not recoverable"
	}
	if _, err := env.Git.Run(ctx, ev.repo.Dir, "worktree", "prune"); err != nil {
		return failedTrash(en, fmt.Errorf("worktree moved to %s but git worktree prune failed: %w", rec.StoredPath, err))
	}
	en.Status = session.StatusApplied
	return en, nil
}

// readdHint is the manual git command that recreates the worktree.
func readdHint(undo map[string]string) string {
	path := shellQuote(undo[undoWT])
	if b := undo[undoBranch]; b != "" {
		return "git worktree add " + path + " " + shellQuote(b)
	}
	return "git worktree add --detach " + path + " " + undo[undoHead]
}
