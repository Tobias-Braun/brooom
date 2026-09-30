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

	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/output"
	"github.com/Tobias-Braun/brooom/internal/procs"
	"github.com/Tobias-Braun/brooom/internal/scope"
	"github.com/Tobias-Braun/brooom/internal/session"
	"github.com/Tobias-Braun/brooom/internal/trash"
)

// metaUserDataRisk is the finding Meta key the large-untracked detector uses
// to say that the path may be the only copy of the user's work.
const (
	metaUserDataRisk  = "user_data_risk"
	userDataUntracked = "untracked"
)

// openFilesFn is the open-file check. It is a variable only so tests can
// inject deterministic answers; production code always uses procs.OpenFiles.
var openFilesFn = procs.OpenFiles

// now stamps entries whose record carries no removal time.
var trashNow = time.Now

// trashAction removes files, directories and symlinks found by the file
// detectors. It is the one place where the scope guard, the open-file check
// and the git safety rules meet the trash strategies.
type trashAction struct{}

func init() { Register(trashAction{}) }

// Type implements Action.
func (trashAction) Type() findings.ActionType { return findings.ActionTrash }

// Plan re-validates the finding in a fixed order: scope, static refusals,
// existence and contents, open files, risk flags, tracked files, and the
// delete-strategy guard. The order matters for the reported reason and keeps
// the cheap checks in front of the expensive ones. Refusals that protect
// against the highest-damage mistakes (roots, .git, nested repositories, open
// files, permanently deleting untracked files) ignore --force; it only lifts
// blocking risk flags and the tracked-files check.
func (trashAction) Plan(ctx context.Context, env *Env, f findings.Finding) (Step, error) {
	path, err := resolveTarget(env, f.Path)
	if err != nil {
		return Step{}, err
	}
	if err := refuseTarget(env, path); err != nil {
		return Step{}, err
	}
	fresh, err := refreshFinding(ctx, f, path)
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
	strategy, err := planStrategy(ctx, env, f, path)
	if err != nil {
		return Step{}, err
	}
	return Step{
		Finding:     fresh,
		Description: describe(strategy, fresh.SizeBytes, path, notes),
		Command:     displayCommand(strategy, path),
	}, nil
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

// checkTracked reports whether the target holds files tracked by git. It
// returns true when tracked files were found (or could not be ruled out) and
// --force allows going on; without force those cases are skips. Targets
// outside any repository have nothing tracked. An error from git means
// unknown, and unknown is never treated as untracked.
func checkTracked(ctx context.Context, env *Env, path string) (bool, error) {
	start := path
	if fi, err := os.Lstat(path); err != nil || !fi.IsDir() {
		start = filepath.Dir(path)
	}
	root, err := scope.FindRepoRoot(start)
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
	if err != nil {
		return unknownTracked(env, fmt.Sprintf("cannot list tracked files: %v", err))
	}
	if strings.Trim(out, "\x00 \n") == "" {
		return false, nil
	}
	if !env.Force {
		return false, skipf("contains files tracked by git")
	}
	return true, nil
}

// unknownTracked is the outcome when tracked files cannot be ruled out.
func unknownTracked(env *Env, why string) (bool, error) {
	if env.Force {
		return true, nil
	}
	return false, skipf("%s, so tracked files cannot be ruled out (use --force to override)", why)
}

// planStrategy resolves the trasher the finding will be removed with and
// enforces the delete-strategy guard: permanent deletion of untracked files
// is refused even with --force, since they may be the only copy of the
// user's work. The finding's Meta hint is honoured, but it is never the
// proof of safety: a step from any caller may carry empty Meta, so permanent
// deletion additionally requires live git state to show that the path holds
// no untracked, non-ignored file. Outside a repository, or when git cannot
// answer, that cannot be shown and the deletion is refused. It returns the
// strategy for the step description.
func planStrategy(ctx context.Context, env *Env, f findings.Finding, path string) (config.TrashStrategy, error) {
	tr, err := trasherFor(env, f.Detector)
	if err != nil {
		return "", err
	}
	strategy := tr.Strategy()
	if strategy != config.StrategyDelete {
		return strategy, nil
	}
	if f.Meta[metaUserDataRisk] == userDataUntracked {
		return "", skipf("refusing to permanently delete untracked files; use --trash-strategy trash or quarantine")
	}
	if err := proveNoUntracked(ctx, env, path); err != nil {
		return "", err
	}
	return strategy, nil
}

// proveNoUntracked succeeds only when git reports no untracked, non-ignored
// file below path. Ignored files are regenerable by definition and tracked
// files are recoverable from history; anything else may be the only copy.
func proveNoUntracked(ctx context.Context, env *Env, path string) error {
	const refuse = "refusing to permanently delete %s; use --trash-strategy trash or quarantine"
	start := path
	if fi, err := os.Lstat(path); err != nil || !fi.IsDir() {
		start = filepath.Dir(path)
	}
	root, err := scope.FindRepoRoot(start)
	if err != nil || env.Git == nil {
		return skipf(refuse, "a path outside a git repository (cannot show that it holds no untracked files)")
	}
	out, err := env.Git.Run(ctx, root, "ls-files", "-z", "--others", "--exclude-standard", "--", path)
	if err != nil {
		return skipf(refuse, "a path git cannot inspect (cannot show that it holds no untracked files)")
	}
	if strings.Trim(out, "\x00 \n") != "" {
		return skipf(refuse, "untracked files")
	}
	return nil
}

func trasherFor(env *Env, detector string) (trash.Trasher, error) {
	if env.Trasher == nil {
		return nil, errors.New("trash: no trasher configured")
	}
	tr, err := env.Trasher(detector)
	if err != nil {
		return nil, fmt.Errorf("trash: choose trash strategy for %s: %w", detector, err)
	}
	return tr, nil
}

// describe renders the one-line step description, e.g.
// "move node_modules (1.2 GB) to trash".
func describe(strategy config.TrashStrategy, size int64, path string, notes []string) string {
	name := filepath.Base(path)
	sz := output.FormatSize(size)
	var d string
	switch strategy {
	case config.StrategyDelete:
		d = fmt.Sprintf("permanently delete %s (%s)", name, sz)
	case config.StrategyQuarantine:
		d = fmt.Sprintf("move %s (%s) to quarantine", name, sz)
	default:
		d = fmt.Sprintf("move %s (%s) to trash", name, sz)
	}
	if len(notes) > 0 {
		d += " [" + strings.Join(notes, "; ") + "]"
	}
	return d
}

// displayCommand is a display-only shell equivalent of the step. Brooom never
// runs it.
func displayCommand(strategy config.TrashStrategy, path string) string {
	q := shellQuote(path)
	switch strategy {
	case config.StrategyDelete:
		return "rm -rf " + q
	case config.StrategyQuarantine:
		dir := "<quarantine dir>"
		if d, err := config.ResolveDirs(); err == nil {
			dir = shellQuote(d.Quarantine)
		}
		return "mv " + q + " " + dir
	default:
		return "trash " + q
	}
}

// shellQuote single-quotes s unless it only holds characters that are safe
// unquoted in a shell.
func shellQuote(s string) string {
	safe := s != ""
	for _, r := range s {
		if !shellSafeRune(r) {
			safe = false
			break
		}
	}
	if safe {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// shellSafeRune reports whether r needs no quoting in a shell word.
func shellSafeRune(r rune) bool {
	alnum := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'
	return alnum || strings.ContainsRune("/._-:+@%", r)
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
	// the delete-strategy guard.
	if err := recheckStep(ctx, env, f, path); err != nil {
		return failedTrash(en, err)
	}
	tr, err := trasherFor(env, f.Detector)
	if err != nil {
		return failedTrash(en, err)
	}
	if tr.Strategy() == config.StrategyDelete && env.BeforeDelete != nil {
		env.BeforeDelete()
	}
	rec, err := tr.Remove(ctx, path)
	if err != nil {
		return removeFailed(en, path, rec, err)
	}
	return appliedEntry(en, rec), nil
}

// recheckStep repeats the live-state checks of Plan: nested repositories,
// open files, tracked files (skipped without --force) and the delete-strategy
// guard. Nothing here trusts the step's Meta or risk flags. A path that
// vanished is left to the caller's existence check, which runs before this
// one. refreshFinding walks the whole tree once more, so a large directory
// is scanned twice per run (Plan, then Apply); that is the price of not
// trusting a step, and the walk is only needed for the nested .git check
// here, its size result is discarded.
func recheckStep(ctx context.Context, env *Env, f findings.Finding, path string) error {
	if _, err := refreshFinding(ctx, f, path); err != nil {
		return err
	}
	if _, err := checkOpen(ctx, path); err != nil {
		return err
	}
	if _, err := checkTracked(ctx, env, path); err != nil {
		return err
	}
	_, err := planStrategy(ctx, env, f, path)
	return err
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
func recoveryHint(rec trash.Record) string {
	switch rec.Strategy {
	case config.StrategyDelete:
		return "not recoverable"
	case config.StrategyQuarantine:
		return "restore from " + rec.StoredPath
	default:
		return "open the Trash/Recycle Bin and use Put Back / Restore"
	}
}

// Undo restores a trashed item. It uses the strategy recorded in the entry,
// not the configured one, so changing the config later never breaks
// restoring old sessions; Env.TrasherFor is the seam for that. The recorded
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
	if env.TrasherFor == nil {
		return errors.New("trash undo: no trasher factory configured")
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
	tr, err := env.TrasherFor(rec.Strategy)
	if err != nil {
		return fmt.Errorf("trash undo: %w", err)
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
