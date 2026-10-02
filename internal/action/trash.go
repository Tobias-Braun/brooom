package action

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/procs"
	"github.com/Tobias-Braun/brooom/internal/scope"
	"github.com/Tobias-Braun/brooom/internal/session"
	"github.com/Tobias-Braun/brooom/internal/trash"
	"github.com/Tobias-Braun/brooom/internal/walk"
)

// openFilesFn is the open-file check. It is a variable only so tests can
// inject deterministic answers; production code always uses procs.OpenFiles.
var openFilesFn = procs.OpenFiles

// now stamps entries whose record carries no removal time.
var trashNow = time.Now

// trashAction removes files, directories and symlinks found by the file
// detectors. It is the one place where the scope guard, the open-file check
// and the git safety rules meet the OS trash.
type trashAction struct{}

func init() { Register(trashAction{}) }

// Type implements Action.
func (trashAction) Type() findings.ActionType { return findings.ActionTrash }

// Plan re-validates the finding in a fixed order: scope, static refusals,
// existence and contents, open files, risk flags and tracked files. The order
// matters for the reported reason and keeps the cheap checks in front of the
// expensive ones. Refusals that protect against the highest-damage mistakes
// (roots, .git, nested repositories, open files) ignore --force; it only
// lifts blocking risk flags and the tracked-files check.
func (trashAction) Plan(ctx context.Context, env *Env, f findings.Finding) (Step, error) {
	path, err := resolveTarget(env, f.Path)
	if err != nil {
		return Step{}, err
	}
	if err := refuseTarget(env, path); err != nil {
		return Step{}, err
	}
	fresh, err := inspectTarget(ctx, env, f, path)
	if err != nil {
		return Step{}, err
	}
	var notes []string
	note, err := checkOpen(ctx, path)
	if err != nil {
		return Step{}, err
	}
	notes = appendNote(notes, note)
	if !findings.Actionable(f.RiskFlags, env.Force) {
		return Step{}, skipf("%s", blockedReason(f.RiskFlags, env.Force))
	}
	tracked, err := checkTracked(ctx, env, path)
	if err != nil {
		return Step{}, err
	}
	if tracked {
		notes = append(notes, "tracked files")
	}
	if _, err := trasherOf(env); err != nil {
		return Step{}, err
	}
	return Step{
		Finding:     fresh,
		Description: describe(path, notes),
		Command:     displayCommand(path),
	}, nil
}

// inspectTarget applies the catalog protect rules to the path and to what it
// contains, then measures it (see refreshFinding). Protection comes first:
// a protected path is refused before anything is walked.
func inspectTarget(ctx context.Context, env *Env, f findings.Finding, path string) (findings.Finding, error) {
	prot, err := newProtection(env, path)
	if err != nil {
		return f, fmt.Errorf("trash: load catalog protect rules: %w", err)
	}
	if reason := prot.target(path); reason != "" {
		return f, skipf("%s", reason)
	}
	return refreshFinding(ctx, f, path, prot)
}

func appendNote(notes []string, note string) []string {
	if note == "" {
		return notes
	}
	return append(notes, note)
}

// isGone reports whether err says the path does not exist.
func isGone(err error) bool { return errors.Is(err, fs.ErrNotExist) }

// checkOpen asks whether a process holds the path open. An open path is
// never acted on. Unknown answers (the check is unavailable, incomplete or
// failed) allow the action, because on most systems the check works only
// partially and blocking on unknown would make trashing unusable, but they
// are returned as a note so the plan says so. A true entry is reliable even
// next to an error, hence the map is consulted first.
func checkOpen(ctx context.Context, path string) (string, error) {
	res, err := openFilesFor(ctx, path)
	if res[path] {
		return "", skipf("file is open by a process")
	}
	switch {
	case err == nil:
		return "", nil
	case errors.Is(err, procs.ErrUnavailable):
		return "open-file check unavailable", nil
	case errors.Is(err, procs.ErrIncomplete):
		return "open-file check incomplete", nil
	default:
		return "open-file check failed", nil
	}
}

// openFilesFor answers from the plan-wide batch when the path is part of it
// and otherwise checks the single path (tests and callers that plan one
// finding at a time, or paths the batch could not vouch for).
func openFilesFor(ctx context.Context, path string) (map[string]bool, error) {
	if b := openBatchFrom(ctx); b != nil {
		if res, ok, err := b.lookup(path); ok {
			return res, err
		}
	}
	return openFilesFn(ctx, []string{path})
}

// repoLookupStart is where the repository lookup for a target begins: the
// target itself when it is a real directory, otherwise its parent. The
// classification is walk.IsDirNoFollow, so a symlink, and on Windows a
// junction or other name-surrogate reparse point, counts as a file (the
// entry that is removed) exactly as walk and the trashers see it, instead of
// being looked through into whatever it points to.
func repoLookupStart(path string) string {
	if walk.IsDirNoFollow(path) {
		return path
	}
	return filepath.Dir(path)
}

// checkTracked reports whether the target holds files tracked by git. It
// returns true when tracked files were found (or could not be ruled out) and
// --force allows going on; without force those cases are skips. Targets
// outside any repository have nothing tracked. An error from git means
// unknown, and unknown is never treated as untracked.
func checkTracked(ctx context.Context, env *Env, path string) (bool, error) {
	if ans, ok := trackedBatchFrom(ctx).lookup(path); ok {
		return trackedVerdict(ctx, env, ans)
	}
	root, err := scope.FindRepoRoot(repoLookupStart(path))
	if errors.Is(err, scope.ErrNotInRepo) {
		return false, nil
	}
	if err != nil {
		return unknownTracked(env, fmt.Sprintf("cannot look up the repository: %v", err))
	}
	if env.Git == nil {
		return unknownTracked(env, "no git runner to check for tracked files")
	}
	out, err := env.Git.Run(ctx, root, "ls-files", "-z", "--", ":(literal)"+path)
	return trackedVerdict(ctx, env, trackedAnswer{
		tracked: strings.Trim(out, "\x00 \n") != "",
		err:     err,
	})
}

// trackedVerdict applies the policy to a (batched or single) answer: errors
// go through lsFilesFailed, tracked files are a skip without --force.
func trackedVerdict(ctx context.Context, env *Env, ans trackedAnswer) (bool, error) {
	if ans.err != nil {
		return lsFilesFailed(ctx, env, ans.err)
	}
	if !ans.tracked {
		return false, nil
	}
	if !env.Force {
		return false, skipf("contains files tracked by git")
	}
	return true, nil
}

// lsFilesFailed maps a failed "git ls-files" to the checkTracked outcome.
func lsFilesFailed(ctx context.Context, env *Env, err error) (bool, error) {
	if ctx.Err() != nil {
		// Ctrl-C killed git: that is no reason to advise --force.
		return false, fmt.Errorf("list tracked files: %w", ctx.Err())
	}
	// A path git places outside the repository (a separate git dir, a
	// bare layout) is not "unknown": it is metadata or foreign data, and
	// --force must not lift the refusal.
	if strings.Contains(err.Error(), "outside repository") {
		return false, skipf("git reports the path is outside its repository (%v)", err)
	}
	return unknownTracked(env, fmt.Sprintf("cannot list tracked files: %v", err))
}

// unknownTracked is the outcome when tracked files cannot be ruled out.
func unknownTracked(env *Env, why string) (bool, error) {
	if env.Force {
		return true, nil
	}
	return false, skipf("%s, so tracked files cannot be ruled out (use --force to override)", why)
}

func trasherOf(env *Env) (trash.Trasher, error) {
	if env.Trasher == nil {
		return nil, errors.New("trash: no trasher configured")
	}
	return env.Trasher, nil
}

// describe renders the one-line step description, e.g. "move node_modules to
// trash". It carries no size: the plan and the prompts print the finding's
// size next to every description, so adding it here would show it twice.
func describe(path string, notes []string) string {
	d := fmt.Sprintf("move %s to trash", filepath.Base(path))
	if len(notes) > 0 {
		d += " [" + strings.Join(notes, "; ") + "]"
	}
	return d
}

// Apply removes the planned path through the configured trasher. The path is
// re-resolved and re-checked against the static refusals first: Plan ran
// earlier and Apply must not trust a step that could have been built by any
// caller. Failures are returned as a failed entry together with the error; a
// path that vanished since planning is a skipped entry, not a failure.
func (trashAction) Apply(ctx context.Context, env *Env, s Step) (session.Entry, error) {
	f := s.Finding
	en := session.Entry{
		FindingID: f.ID, Detector: f.Detector, Action: findings.ActionTrash,
		Path: f.Path, Ref: f.Ref, SizeBytes: f.SizeBytes,
	}
	path, err := resolveTarget(env, f.Path)
	if err == nil {
		en.Path = path
		err = refuseTarget(env, path)
	}
	if err != nil {
		return failedTrash(en, err)
	}
	if _, err := os.Lstat(path); isGone(err) {
		return goneEntry(en), nil
	}
	// A step may come from any caller, so the checks that Plan makes
	// against live state run again: nested repositories, open files and
	// tracked files.
	size, err := recheckStep(ctx, env, f, path)
	if err != nil {
		return failedTrash(en, err)
	}
	tr, err := trasherOf(env)
	if err != nil {
		return failedTrash(en, err)
	}
	// The size of the walk that just re-validated the tree goes to the
	// trasher, so it neither walks again nor reports a different number than
	// the plan showed.
	rec, err := tr.Remove(trash.WithSizeHint(ctx, path, size), path)
	if err != nil {
		return removeFailed(en, path, rec, err)
	}
	return appliedEntry(en, rec), nil
}

// recheckStep repeats the live-state checks of Plan: nested repositories,
// open files and tracked files (skipped without --force). Nothing here trusts the step's Meta or risk flags. A path that
// vanished is left to the caller's existence check, which runs before this
// one. refreshFinding walks the whole tree once more, so a large directory
// is scanned twice per run (Plan, then Apply); that is the price of not
// trusting a step. The walk's size is returned and handed to the trasher, so
// Remove does not measure a third time.
func recheckStep(ctx context.Context, env *Env, f findings.Finding, path string) (int64, error) {
	fresh, err := inspectTarget(ctx, env, f, path)
	if err != nil {
		return 0, err
	}
	if _, err := checkOpen(ctx, path); err != nil {
		return 0, err
	}
	if _, err := checkTracked(ctx, env, path); err != nil {
		return 0, err
	}
	return fresh.SizeBytes, nil
}

func failedTrash(en session.Entry, err error) (session.Entry, error) {
	en.Status = session.StatusFailed
	en.Error = err.Error()
	en.At = trashNow().UTC()
	return en, err
}

func goneEntry(en session.Entry) session.Entry {
	en.Status = session.StatusSkipped
	en.Error = "already gone"
	en.At = trashNow().UTC()
	return en
}

// removeFailed classifies a failing Remove. A path that disappeared between
// the stat and the move is a skip. A trasher may return a record together
// with its error (a complete copy was stored but the source could not be
// fully removed); keeping it in the entry preserves the way to that copy.
func removeFailed(en session.Entry, path string, rec trash.Record, err error) (session.Entry, error) {
	if isGone(err) {
		if _, statErr := os.Lstat(path); isGone(statErr) {
			return goneEntry(en), nil
		}
	}
	if rec.StoredPath != "" || rec.OriginalPath != "" {
		en.Trash = &rec
		if rec.StoredPath != "" {
			en.RecoveryHint = "a copy was stored at " + rec.StoredPath
		}
	}
	return failedTrash(en, fmt.Errorf("trash %s: %w", path, err))
}

func appliedEntry(en session.Entry, rec trash.Record) session.Entry {
	en.Status = session.StatusApplied
	en.Trash = &rec
	en.SizeBytes = rec.SizeBytes
	en.Restorable = rec.Restorable
	en.RecoveryHint = recoveryHint(rec)
	en.At = rec.RemovedAt.UTC()
	if rec.RemovedAt.IsZero() {
		en.At = trashNow().UTC()
	}
	return en
}

// recoveryHint tells the user how to get an item back without Brooom.
func recoveryHint(trash.Record) string {
	return "open the Trash/Recycle Bin and use Put Back / Restore"
}

// Undo restores a trashed item from the OS trash. Entries that earlier
// releases removed another way never get here (see notUndoable). The recorded
// original path is re-validated against the guard first, because a manifest
// is a file the user (or something else) can edit: a forged entry must not
// make Brooom write outside the allowed roots. ErrRestoreConflict and
// ErrNotRestorable from the trasher are returned unchanged for the undo
// command to report.
func (trashAction) Undo(ctx context.Context, env *Env, e session.Entry) error {
	if e.Trash == nil {
		return errors.New("trash undo: entry has no trash record")
	}
	if e.Status != session.StatusApplied {
		return fmt.Errorf("trash undo: entry for %s is %s, only applied entries can be undone", e.Path, e.Status)
	}
	tr, err := trasherOf(env)
	if err != nil {
		return err
	}
	rec := *e.Trash
	if !filepath.IsAbs(rec.OriginalPath) {
		return fmt.Errorf("trash undo: refusing to restore to relative path %q", rec.OriginalPath)
	}
	dest, err := resolveRestoreTarget(env, rec.OriginalPath)
	if err != nil {
		return err
	}
	if err := refuseRestoreTarget(dest); err != nil {
		return err
	}
	rec.OriginalPath = dest
	if err := checkRecordPaths(rec); err != nil {
		return err
	}
	return tr.Restore(ctx, rec)
}

// resolveRestoreTarget checks that the restore destination lies inside the
// allowed roots and returns it in its resolved spelling.
func resolveRestoreTarget(env *Env, path string) (string, error) {
	if env.Guard == nil {
		return "", errors.New("trash undo: no scope guard configured")
	}
	dest, err := env.Guard.ResolveParent(path)
	if err != nil {
		return "", fmt.Errorf("trash undo: refusing to restore to %s: %w", path, err)
	}
	return dest, nil
}
