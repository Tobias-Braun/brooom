package action

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/gitx"
	"github.com/Tobias-Braun/brooom/internal/output"
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

// removeWorktree removes a leftover linked worktree.
//
// The directory always goes through the configured trasher, so files git
// ignores (.env, agent settings, logs, build output) reach the trash or
// quarantine and undo brings them back; `git worktree remove` would delete
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
	if err := checkUnmodified(ctx, path, f); err != nil {
		return nil, err
	}
	ev := &removeEval{repo: repo, wt: wt, path: path}
	if err := ev.inspect(ctx, env); err != nil {
		return nil, err
	}
	if err := ev.checkDirty(env, f); err != nil {
		return nil, err
	}
	if !findings.Actionable(f.RiskFlags, env.Force) {
		return nil, skipf("%s", blockedReason(f.RiskFlags, env.Force))
	}
	return ev, nil
}

// checkUnmodified refuses when any file below the worktree is newer than the
// LastModified the scan recorded. git status ignores gitignored files, so an
// edit of build output or an env file after the scan (or one a stale cache hid
// during it) is visible only through mtimes, read here with a Fresh walk. A
// finding without LastModified has no baseline and is not checked; a walk that
// fails is refused, because unknown must never read as unchanged. Not
// overridable by --force: a rescan is the way forward.
func checkUnmodified(ctx context.Context, path string, f findings.Finding) error {
	if f.LastModified == nil {
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
	case m.nestedGit != "":
		return skipf("contains a git repository (.git at %s)", m.nestedGit)
	}
	if ev.dirty, err = ev.repo.IsDirty(ctx, ev.path); err != nil {
		return fmt.Errorf("worktree: check %s for uncommitted changes: %w", ev.path, err)
	}
	if ev.ignored, err = ev.repo.IgnoredEntries(ctx, ev.path); err != nil {
		return fmt.Errorf("worktree: list ignored files of %s: %w", ev.path, err)
	}
	return nil
}

// checkDirty applies the trasher rules. Without --force a dirty worktree is
// skipped. The delete strategy is refused for a worktree with uncommitted or
// ignored files, since either may be the only copy of the user's work.
func (ev *removeEval) checkDirty(env *Env, f findings.Finding) error {
	if ev.dirty && !env.Force {
		return skipf("worktree has uncommitted changes; re-run with --force to trash it")
	}
	tr, err := trasherFor(env, f.Detector)
	if err != nil {
		return err
	}
	if tr.Strategy() == config.StrategyDelete {
		switch {
		case ev.dirty:
			return skipf("refusing to permanently delete uncommitted work; use --trash-strategy trash or quarantine")
		case len(ev.ignored) > 0:
			return skipf("refusing to permanently delete files ignored by git (%s); use --trash-strategy trash or quarantine", ignoredSummary(ev.ignored))
		}
		return nil
	}
	ev.trasher = tr
	return nil
}

// ignoredSummary names the first few ignored entries for messages.
func ignoredSummary(entries []string) string {
	const show = 3
	if len(entries) <= show {
		return strings.Join(entries, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(entries[:show], ", "), len(entries)-show)
}

// Plan implements Action.
func (removeWorktree) Plan(ctx context.Context, env *Env, f findings.Finding) (Step, error) {
	ev, err := evaluateRemove(ctx, env, f)
	if err != nil {
		return Step{}, err
	}
	fresh := f
	fresh.Path = ev.path
	if ev.trasher == nil {
		return Step{
			Finding: fresh,
			Description: fmt.Sprintf("remove worktree %s (%s) with git worktree remove",
				filepath.Base(ev.path), output.FormatSize(f.SizeBytes)) + noteSuffix(ev.note),
			Command: "git worktree remove -- " + shellQuote(ev.path),
		}, nil
	}
	strategy := ev.trasher.Strategy()
	return Step{
		Finding:     fresh,
		Description: describe(strategy, f.SizeBytes, ev.path, ev.notes()) + noteSuffix(ev.note),
		Command:     displayCommand(strategy, ev.path) + " && git worktree remove -- " + shellQuote(ev.path),
	}, nil
}

// notes flags what git's own removal would have lost or what needs care.
func (ev *removeEval) notes() []string {
	notes := []string{"worktree"}
	if ev.dirty {
		notes = append(notes, "uncommitted changes")
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
	undo := map[string]string{
		undoRepo: ev.repo.Dir, undoWT: ev.path, undoBranch: ev.wt.Branch, undoHead: ev.wt.Head,
	}
	if ev.trasher != nil {
		en.Undo = undo
		return ev.applyTrashed(ctx, env, en)
	}
	return ev.applyClean(ctx, env, en, undo)
}

// applyClean removes a worktree without uncommitted or ignored files with
// plain `git worktree remove`; it is only reached under the delete strategy,
// where nothing is left to lose. git's own refusals (submodules, untracked files that appeared since the
// check, a lock taken meanwhile) surface as the failure message.
func (ev *removeEval) applyClean(ctx context.Context, env *Env, en session.Entry, undo map[string]string) (session.Entry, error) {
	if _, err := env.Git.Run(ctx, ev.repo.Dir, "worktree", "remove", "--", ev.path); err != nil {
		return failedTrash(en, fmt.Errorf("git worktree remove %s: %w", ev.path, err))
	}
	// Undo data is attached only once the removal happened; a failed removal
	// leaves nothing to undo.
	en.Undo = undo
	en.Status = session.StatusApplied
	en.Restorable = true
	en.RecoveryHint = readdHint(en.Undo)
	return en, nil
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
	en.RecoveryHint = "restore the directory from the trash record (brooom undo does it), then " + readdHint(en.Undo)
	if ev.dirty {
		en.RecoveryHint += "; the staged/unstaged split of the uncommitted work is not restored, all changes reappear as unstaged"
	}
	if !rec.Restorable {
		en.RecoveryHint = "not recoverable: the " + string(rec.Strategy) + " trash strategy keeps no restorable copy of the worktree"
	}
	if _, err := env.Git.Run(ctx, ev.repo.Dir, "worktree", "remove", "--", ev.path); err != nil {
		return failedTrash(en, fmt.Errorf("worktree moved to %s but git worktree remove of the registration failed: %w", rec.StoredPath, err))
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
