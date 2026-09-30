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
// gone. The registration is dropped with `git worktree remove --force -- <path>`
// because, unlike the repository-wide `git worktree prune`, it touches exactly
// the finding's own entry: a registration the user declined (or that has no
// finding) must survive, since its admin dir holds HEAD, the reflog and the
// staged index. --force is needed for git to accept a missing directory and
// does not lift locks (that would take a second --force, which is never
// passed). As a safety net Apply lists the worktrees before and after and
// fails loudly if anything other than the target disappeared.
type pruneWorktrees struct{}

// Type implements Action.
func (pruneWorktrees) Type() findings.ActionType { return findings.ActionPruneWorktrees }

// evaluatePrune checks that the entry recorded for f.Path is still prunable.
// The path is matched as recorded, without resolving it: the directory is
// missing, and pruning only edits the repository's git directory.
func evaluatePrune(ctx context.Context, env *Env, f findings.Finding) (*gitx.Repo, gitx.Worktree, error) {
	repo, err := openWorktreeRepo(ctx, env, f)
	if err != nil {
		return nil, gitx.Worktree{}, err
	}
	list, err := repo.ListWorktrees(ctx)
	if err != nil {
		return nil, gitx.Worktree{}, fmt.Errorf("worktree: list worktrees of %s: %w", repo.Dir, err)
	}
	wt, ok := findWorktree(list, f.Path)
	// A directory that exists again (even without its .git file, which git
	// still calls prunable) is somebody's content now and is left alone.
	if !ok || wt.Main || !wt.Prunable || wt.Locked || !wt.DirMissing {
		return nil, wt, skipf("not prunable any more")
	}
	if !findings.Actionable(f.RiskFlags, env.Force) {
		return nil, wt, skipf("%s", blockedReason(f.RiskFlags, env.Force))
	}
	if err := checkDetachedHead(ctx, env, repo, wt); err != nil {
		return nil, wt, err
	}
	return repo, wt, nil
}

// checkDetachedHead refuses to drop the registration of a detached worktree
// whose HEAD commit no branch, remote branch or tag holds: HEAD and its reflog
// live in the admin dir that the removal deletes, so those commits would
// become unreachable. The detector already withholds the action for such
// worktrees, but a finding may come from a report file or an older version,
// so apply time re-checks. A failing check counts as unknown and refuses.
//
// The two checks differ on purpose. The action asks only "would the commit
// survive the removal?", so any local branch, remote branch or tag qualifies:
// each keeps the objects reachable and the user's own branch is theirs to
// keep. The detector asks the stricter "is the work safely published?" and
// accepts only the base branch or a remote, because it recommends deleting.
func checkDetachedHead(ctx context.Context, env *Env, repo *gitx.Repo, wt gitx.Worktree) error {
	if !wt.Detached || wt.Head == "" {
		return nil
	}
	out, err := env.Git.Run(ctx, repo.Dir, "for-each-ref", "--count=1", "--format=%(refname)",
		"--contains", wt.Head, "refs/heads", "refs/remotes", "refs/tags")
	if err != nil {
		return skipf("cannot verify that the detached HEAD %s is held by a branch: %v", wt.Head, err)
	}
	if strings.TrimSpace(out) == "" {
		return skipf("detached HEAD %s is held by no branch or tag and would become unreachable; "+
			"run git worktree repair <new path> if the directory only moved, or git branch rescue %s first", wt.Head, wt.Head)
	}
	return nil
}

// Plan implements Action.
func (pruneWorktrees) Plan(ctx context.Context, env *Env, f findings.Finding) (Step, error) {
	if _, _, err := evaluatePrune(ctx, env, f); err != nil {
		return Step{}, err
	}
	return Step{
		Finding:     f,
		Description: fmt.Sprintf("prune git metadata of missing worktree %s", filepath.Base(f.Path)),
		Command:     "git worktree remove --force -- " + findings.Quote(f.Path),
	}, nil
}

// Apply implements Action.
func (pruneWorktrees) Apply(ctx context.Context, env *Env, s Step) (session.Entry, error) {
	f := s.Finding
	en := session.Entry{
		FindingID: f.ID, Detector: f.Detector, Action: findings.ActionPruneWorktrees,
		Path: f.Path, Ref: f.Ref, At: trashNow().UTC(),
	}
	repo, wt, err := evaluatePrune(ctx, env, f)
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
	if _, err := env.Git.Run(ctx, repo.Dir, "worktree", "remove", "--force", "--", wt.Path); err != nil {
		return failedTrash(en, fmt.Errorf("git worktree remove --force %s: %w", wt.Path, err))
	}
	after, err := repo.ListWorktrees(ctx)
	if err != nil {
		return failedTrash(en, fmt.Errorf("worktree: list worktrees of %s after pruning: %w", repo.Dir, err))
	}
	if err := verifyPruned(before, after, wt.Path); err != nil {
		return failedTrash(en, err)
	}
	// The undo data is kept for the manual recovery only: Restorable stays
	// false, and Undo refuses, because nothing can be put back automatically.
	en.Undo = map[string]string{undoRepo: repo.Dir, undoWT: wt.Path, undoBranch: wt.Branch, undoHead: wt.Head}
	en.Status = session.StatusApplied
	en.RecoveryHint = pruneHint(wt)
	return en, nil
}

// verifyPruned compares the lists around the removal: the target must be gone
// and it must be the only entry that vanished, whatever its state. Anything
// else means a registration the user did not confirm was lost.
func verifyPruned(before, after []gitx.Worktree, target string) error {
	for _, w := range before {
		if _, still := findWorktree(after, w.Path); still {
			continue
		}
		if !gitx.SamePath(w.Path, target) {
			return fmt.Errorf("worktree: removing %s also removed the registration of %s; check the repository", target, w.Path)
		}
	}
	if _, still := findWorktree(after, target); still {
		return fmt.Errorf("worktree: %s is still registered after git worktree remove", target)
	}
	return nil
}

// pruneHint explains what was removed (metadata only) and how to bring the
// entry back. The admin dir held HEAD, the reflog and the staged index, so for
// a detached worktree the commit is unreachable now (it survives as a loose
// object until gc) and must be rescued with a branch.
func pruneHint(wt gitx.Worktree) string {
	hint := "only git metadata for the missing directory was removed (uncommitted state in an unmounted directory is untouched on disk, " +
		"but staged changes kept in git's admin directory are gone); to re-create: " +
		readdHint(map[string]string{undoWT: wt.Path, undoBranch: wt.Branch, undoHead: wt.Head})
	if wt.Branch == "" && wt.Head != "" {
		hint += "; the detached HEAD " + wt.Head + " is unreachable now unless a ref holds it, keep it with: git branch rescue " + wt.Head
	}
	return hint
}

// Undo implements Action. Pruned metadata carries no content that could be
// put back, so nothing is restorable.
func (pruneWorktrees) Undo(context.Context, *Env, session.Entry) error {
	return errors.New("prune-worktrees cannot be undone: only git metadata for missing directories was removed; re-create a worktree with git worktree add")
}
