package action

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/gitx"
	"github.com/Tobias-Braun/brooom/internal/session"
	"github.com/Tobias-Braun/brooom/internal/trash"
)

// undoData is the validated content of a remove-worktree manifest entry.
type undoData struct {
	repo, path, branch, head string
}

// undoable reports whether the entry may be undone. An entry that failed only
// because the follow-up prune failed still holds the trashed directory.
func undoable(e session.Entry) error {
	if e.Status == session.StatusApplied || (e.Status == session.StatusFailed && e.Trash != nil) {
		return nil
	}
	return fmt.Errorf("worktree undo: entry for %s is %s, only applied entries can be undone", e.Path, e.Status)
}

// undoTarget validates and unpacks the manifest data of a remove-worktree
// entry. The manifest is an editable file, so the paths are re-checked
// against the guard before anything is written.
func undoTarget(env *Env, e session.Entry) (undoData, error) {
	if err := undoable(e); err != nil {
		return undoData{}, err
	}
	d := undoData{repo: e.Undo[undoRepo], path: e.Undo[undoWT], branch: e.Undo[undoBranch], head: e.Undo[undoHead]}
	if d.repo == "" || d.path == "" || d.head == "" {
		return undoData{}, errors.New("worktree undo: entry lacks the repository, worktree or head to restore")
	}
	if env.Guard == nil || env.Git == nil {
		return undoData{}, errors.New("worktree undo: no scope guard or git runner configured")
	}
	if !filepath.IsAbs(d.path) || !filepath.IsAbs(d.repo) {
		return undoData{}, fmt.Errorf("worktree undo: refusing relative path in manifest (%q, %q)", d.repo, d.path)
	}
	resolved, err := env.Guard.ResolveParent(d.path)
	if err != nil {
		return undoData{}, fmt.Errorf("worktree undo: refusing to restore to %s: %w", d.path, err)
	}
	d.path = resolved
	return d, nil
}

// Undo implements Action. Clean removals are re-added from the recorded
// branch or commit; trashed ones are restored from the trash (undoTrashed).
func (removeWorktree) Undo(ctx context.Context, env *Env, e session.Entry) error {
	d, err := undoTarget(env, e)
	if err != nil {
		return err
	}
	repo, err := openRepoDir(ctx, env, d.repo)
	if err != nil {
		return fmt.Errorf("worktree undo: %w", err)
	}
	if e.Trash != nil {
		return undoTrashed(ctx, env, repo, *e.Trash, d)
	}
	if err := pathFreeForWorktree(d.path); err != nil {
		return err
	}
	return readdWorktree(ctx, env, repo, d, false)
}

// pathFreeForWorktree accepts a missing path or an empty directory, which is
// what git worktree add itself accepts.
func pathFreeForWorktree(path string) error {
	entries, err := os.ReadDir(path)
	if isGone(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("worktree undo: cannot inspect %s: %w", path, err)
	}
	if len(entries) > 0 {
		return fmt.Errorf("worktree undo: %s already exists and is not empty", path)
	}
	return nil
}

// readdWorktree runs git worktree add. The branch form is used when the
// branch exists and is not checked out elsewhere; otherwise the recorded
// commit is checked out detached, provided it still exists.
func readdWorktree(ctx context.Context, env *Env, repo *gitx.Repo, d undoData, noCheckout bool) error {
	path, branch, head := d.path, d.branch, d.head
	args := []string{"worktree", "add"}
	if noCheckout {
		args = append(args, "--no-checkout")
	}
	useBranch, err := branchUsable(ctx, env, repo, branch)
	if err != nil {
		return err
	}
	if useBranch {
		args = append(args, "--", path, branch)
	} else {
		if _, err := env.Git.Run(ctx, repo.Dir, "cat-file", "-e", head+"^{commit}"); err != nil {
			return fmt.Errorf("worktree undo: branch %s is unavailable and commit %s no longer exists (git has garbage-collected it): %w",
				refLabel(branch), head, err)
		}
		args = append(args, "--detach", "--", path, head)
	}
	if _, err := env.Git.Run(ctx, repo.Dir, args...); err != nil {
		return fmt.Errorf("worktree undo: %w", err)
	}
	return nil
}

// branchUsable reports whether branch exists and no worktree has it checked
// out. An empty branch (the worktree was detached) is never usable.
func branchUsable(ctx context.Context, env *Env, repo *gitx.Repo, branch string) (bool, error) {
	if branch == "" {
		return false, nil
	}
	ref := "refs/heads/" + branch
	if _, err := env.Git.Run(ctx, repo.Dir, "rev-parse", "--verify", "--quiet", ref); err != nil {
		var gerr *gitx.Error
		if errors.As(err, &gerr) {
			return false, nil
		}
		return false, fmt.Errorf("worktree undo: look up branch %s: %w", branch, err)
	}
	list, err := repo.ListWorktrees(ctx)
	if err != nil {
		return false, fmt.Errorf("worktree undo: list worktrees: %w", err)
	}
	for _, w := range list {
		if w.BranchRef == ref {
			return false, nil
		}
	}
	return true, nil
}

// undoTrashed restores a worktree that was moved to the trash:
//
//  1. git worktree add --no-checkout recreates the administrative directory
//     at the original path (cheap, no files are written except a .git file),
//  2. the placeholder directory, which must hold nothing but that .git file,
//     is removed so the original path is free,
//  3. the trasher puts the real directory back (conflicts and missing trash
//     copies are passed through as trash.ErrRestoreConflict/ErrNotRestorable),
//  4. git worktree repair reconnects the link files and a mixed reset
//     rebuilds the index from HEAD without touching working files.
//
// The reset means the staged/unstaged split of the uncommitted work is not
// restored: every change reappears as unstaged or untracked, none is lost.
// A failure before step 3 finishes rolls back the registration from step 1
// and leaves the trash copy untouched.
func undoTrashed(ctx context.Context, env *Env, repo *gitx.Repo, rec trash.Record, d undoData) error {
	path := d.path
	if !gitx.SamePath(rec.OriginalPath, path) {
		return fmt.Errorf("worktree undo: trash record path %s does not match worktree %s", rec.OriginalPath, path)
	}
	if env.TrasherFor == nil {
		return errors.New("worktree undo: no trasher factory configured")
	}
	tr, err := env.TrasherFor(rec.Strategy)
	if err != nil {
		return fmt.Errorf("worktree undo: %w", err)
	}
	if err := pathFreeForWorktree(path); err != nil {
		return err
	}
	if err := readdWorktree(ctx, env, repo, d, true); err != nil {
		return err
	}
	admin, err := removePlaceholder(path)
	if err == nil {
		rec.OriginalPath = path
		err = tr.Restore(ctx, rec)
	}
	if err != nil {
		rollbackRegistration(repo, admin)
		return fmt.Errorf("worktree undo: %w (the trashed copy is untouched)", err)
	}
	if _, err := env.Git.Run(ctx, repo.Dir, "worktree", "repair", path); err != nil {
		return fmt.Errorf("worktree undo: files restored to %s but git worktree repair failed: %w", path, err)
	}
	if _, err := env.Git.Run(ctx, path, "reset", "-q"); err != nil {
		return fmt.Errorf("worktree undo: files restored to %s but rebuilding the index failed: %w", path, err)
	}
	return nil
}

// removePlaceholder deletes the directory git worktree add --no-checkout
// created, after checking that it holds only a .git file, and returns the
// administrative directory that file points to. Plain os.Remove is used on
// purpose: it cannot delete anything that is not empty.
func removePlaceholder(path string) (string, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return "", fmt.Errorf("inspect placeholder %s: %w", path, err)
	}
	if len(entries) != 1 || !isGitName(entries[0].Name()) || !entries[0].Type().IsRegular() {
		return "", fmt.Errorf("placeholder %s holds more than a .git file; not removing it", path)
	}
	gitFile := filepath.Join(path, entries[0].Name())
	data, err := os.ReadFile(gitFile)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", gitFile, err)
	}
	admin := ""
	if v, ok := strings.CutPrefix(strings.TrimSpace(string(data)), "gitdir:"); ok {
		admin = gitx.NormalizePath(strings.TrimSpace(v))
	}
	if err := os.Remove(gitFile); err != nil {
		return admin, err
	}
	return admin, os.Remove(path)
}

// rollbackRegistration removes the administrative directory created by the
// failed undo, but only when it really is an entry below the repository's
// worktrees directory. Best effort: a leftover is prunable metadata that
// git worktree prune clears.
func rollbackRegistration(repo *gitx.Repo, admin string) {
	if admin == "" {
		return
	}
	base := filepath.Join(gitx.NormalizePath(repo.Common), "worktrees")
	if findings.IsWithin(base, admin) {
		_ = os.RemoveAll(admin)
	}
}
