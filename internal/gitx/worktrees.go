package gitx

import (
	"context"
	"os"
	"path/filepath"
	"strings"
)

// Worktree is one entry of `git worktree list`.
type Worktree struct {
	Path        string
	Head        string
	Branch      string // short name, empty when detached or bare
	BranchRef   string // full ref, e.g. refs/heads/feat/x
	Bare        bool
	Detached    bool
	Locked      bool
	LockReason  string
	Prunable    bool
	PruneReason string
	// Main is true for the first entry, the main worktree.
	Main bool
	// DirMissing is true when the directory no longer exists on disk.
	DirMissing bool
}

// ListWorktrees parses `git worktree list --porcelain -z`. Git older than
// 2.36 rejects -z, so the newline porcelain is used there (paths containing
// newlines are unsupported on that path). DirMissing comes from a read-only
// os.Stat; with git older than 2.31, which does not report "prunable", a
// missing unlocked directory is marked Prunable as well.
func (r *Repo) ListWorktrees(ctx context.Context) ([]Worktree, error) {
	return cached(r, &r.worktrees, struct{}{}, func() ([]Worktree, error) {
		v := r.gitVersion(ctx)
		args := []string{"worktree", "list", "--porcelain"}
		sep := "\n"
		if v.AtLeast(2, 36) {
			args = append(args, "-z")
			sep = "\x00"
		}
		out, err := r.run(ctx, args...)
		if err != nil {
			return nil, err
		}
		wts := parseWorktrees(out, sep)
		for i := range wts {
			_, statErr := os.Stat(wts[i].Path)
			wts[i].DirMissing = os.IsNotExist(statErr)
			if wts[i].DirMissing && !v.AtLeast(2, 31) && !wts[i].Locked {
				wts[i].Prunable = true
			}
		}
		if len(wts) > 0 {
			wts[0].Main = true
		}
		return wts, nil
	})
}

// parseWorktrees splits the porcelain output into tokens by sep. A "worktree"
// token starts a new record and blank tokens (record separators) are ignored,
// which works identically for the -z and the newline format.
func parseWorktrees(out, sep string) []Worktree {
	var wts []Worktree
	for _, tok := range strings.Split(out, sep) {
		tok = strings.TrimSuffix(tok, "\r")
		key, val, _ := strings.Cut(tok, " ")
		if key == "worktree" {
			wts = append(wts, Worktree{Path: NormalizePath(val)})
			continue
		}
		if len(wts) == 0 {
			continue
		}
		applyWorktreeField(&wts[len(wts)-1], key, val)
	}
	return wts
}

func applyWorktreeField(w *Worktree, key, val string) {
	switch key {
	case "HEAD":
		w.Head = val
	case "branch":
		w.BranchRef = val
		w.Branch = strings.TrimPrefix(val, "refs/heads/")
	case "bare":
		w.Bare = true
	case "detached":
		w.Detached = true
	case "locked":
		w.Locked, w.LockReason = true, val
	case "prunable":
		w.Prunable, w.PruneReason = true, val
	}
}

// IsDirty reports whether the worktree at dir has modified, staged, deleted
// or untracked (non-ignored) files. `--no-optional-locks` and the runner's
// GIT_OPTIONAL_LOCKS=0 keep status from refreshing the index on disk.
func (r *Repo) IsDirty(ctx context.Context, dir string) (bool, error) {
	out, err := r.Runner.Run(ctx, dir, "--no-optional-locks", "status", "--porcelain", "-z", "--untracked-files=normal")
	if err != nil {
		return false, err
	}
	return out != "", nil
}

// IgnoredEntries lists what git ignores below the worktree at dir, as
// slash-separated paths with fully ignored directories collapsed to one entry
// (trailing slash). Status never reports these, but they can hold the only
// copy of local configuration, credentials or agent state.
func (r *Repo) IgnoredEntries(ctx context.Context, dir string) ([]string, error) {
	out, err := r.Runner.Run(ctx, dir, "--no-optional-locks", "ls-files", "-z", "--others", "--ignored", "--exclude-standard", "--directory")
	if err != nil {
		return nil, err
	}
	var entries []string
	for _, e := range strings.Split(out, "\x00") {
		if e != "" {
			entries = append(entries, e)
		}
	}
	return entries, nil
}

// cleanNative converts forward slashes to the native separator and cleans.
func cleanNative(p string) string {
	return filepath.Clean(filepath.FromSlash(p))
}
