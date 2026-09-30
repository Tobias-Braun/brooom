package action

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/gitx"
	"github.com/Tobias-Braun/brooom/internal/session"
)

// pruneWorktrees removes git's metadata for worktrees whose directory is
// gone. `git worktree prune` cannot be limited to one entry, so the safety
// net is verification: Apply lists the worktrees before and after and fails
// loudly if anything that was not prunable (or was locked) disappeared. The
// executor re-plans before every step, so once the first prune has removed
// all missing entries, later findings are skipped as no longer prunable
// rather than failed.
type pruneWorktrees struct{}

// Type implements Action.
func (pruneWorktrees) Type() findings.ActionType { return findings.ActionPruneWorktrees }

// evaluatePrune checks that the entry recorded for f.Path is still prunable.
// The path is matched as recorded, without resolving it: the directory is
// missing, and pruning only edits the repository's git directory.
func evaluatePrune(ctx context.Context, env *Env, f findings.Finding) (*gitx.Repo, error) {
	repo, err := openRepo(ctx, env, f)
	if err != nil {
		return nil, err
	}
	list, err := repo.ListWorktrees(ctx)
	if err != nil {
		return nil, fmt.Errorf("worktree: list worktrees of %s: %w", repo.Dir, err)
	}
	wt, ok := findWorktree(list, f.Path)
	// A directory that exists again (even without its .git file, which git
	// still calls prunable) is somebody's content now and is left alone.
	if !ok || wt.Main || !wt.Prunable || wt.Locked || !wt.DirMissing {
		return nil, skipf("not prunable any more")
	}
	if !findings.Actionable(f.RiskFlags, env.Force) {
		return nil, skipf("%s", blockedReason(f.RiskFlags, env.Force))
	}
	return repo, nil
}

// Plan implements Action.
func (pruneWorktrees) Plan(ctx context.Context, env *Env, f findings.Finding) (Step, error) {
	if _, err := evaluatePrune(ctx, env, f); err != nil {
		return Step{}, err
	}
	return Step{
		Finding:     f,
		Description: fmt.Sprintf("prune git metadata of missing worktree %s", filepath.Base(f.Path)),
		Command:     "git worktree prune",
	}, nil
}

// Apply implements Action.
func (pruneWorktrees) Apply(ctx context.Context, env *Env, s Step) (session.Entry, error) {
	f := s.Finding
	en := session.Entry{
		FindingID: f.ID, Detector: f.Detector, Action: findings.ActionPruneWorktrees,
		Path: f.Path, Ref: f.Ref, At: trashNow().UTC(),
	}
	repo, err := evaluatePrune(ctx, env, f)
	if errors.Is(err, ErrSkipped) {
		en.Status, en.Error = session.StatusSkipped, skipReason(err)
		return en, nil
	}
	if err != nil {
		return failedTrash(en, err)
	}
	before, err := repo.ListWorktrees(ctx)
	if err != nil {
		return failedTrash(en, fmt.Errorf("worktree: list worktrees of %s before pruning: %w", repo.Dir, err))
	}
	if _, err := env.Git.Run(ctx, repo.Dir, "worktree", "prune"); err != nil {
		return failedTrash(en, fmt.Errorf("git worktree prune in %s: %w", repo.Dir, err))
	}
	after, err := repo.ListWorktrees(ctx)
	if err != nil {
		return failedTrash(en, fmt.Errorf("worktree: list worktrees of %s after pruning: %w", repo.Dir, err))
	}
	removed, err := verifyPruned(before, after, f.Path)
	if err != nil {
		return failedTrash(en, err)
	}
	en.Status = session.StatusApplied
	en.RecoveryHint = pruneHint(removed)
	return en, nil
}

// verifyPruned compares the lists around the prune. The target must be gone
// and every vanished entry must have been prunable and unlocked before.
func verifyPruned(before, after []gitx.Worktree, target string) ([]gitx.Worktree, error) {
	var removed []gitx.Worktree
	for _, w := range before {
		if _, still := findWorktree(after, w.Path); still {
			continue
		}
		if !w.Prunable || w.Locked || w.Main {
			return removed, fmt.Errorf("worktree: git worktree prune removed %s, which was not prunable; check the repository", w.Path)
		}
		removed = append(removed, w)
	}
	if _, still := findWorktree(after, target); still {
		return removed, fmt.Errorf("worktree: %s is still registered after git worktree prune", target)
	}
	return removed, nil
}

// pruneHint explains what was removed (metadata only) and how to bring an
// entry back. Every entry that disappeared is named, since the prune could
// not be limited to the finding's own.
func pruneHint(removed []gitx.Worktree) string {
	names := make([]string, 0, len(removed))
	for _, w := range removed {
		names = append(names, readdHint(map[string]string{undoWT: w.Path, undoBranch: w.Branch, undoHead: w.Head}))
	}
	return "only git metadata for missing directories was removed (uncommitted state in an unmounted directory is untouched on disk); " +
		"to re-create: " + strings.Join(names, "; ")
}

// Undo implements Action. Pruned metadata carries no content that could be
// put back, so nothing is restorable.
func (pruneWorktrees) Undo(context.Context, *Env, session.Entry) error {
	return errors.New("prune-worktrees cannot be undone: only git metadata for missing directories was removed; re-create a worktree with git worktree add")
}
