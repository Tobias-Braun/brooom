package action

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/scope"
	"github.com/Tobias-Braun/brooom/internal/session"
	"github.com/Tobias-Braun/brooom/internal/trash"
)

// UndoKind classifies what `brooom undo` can do with one manifest entry.
type UndoKind string

const (
	// UndoRestore means the entry will be handed to its action's Undo.
	UndoRestore UndoKind = "restore"
	// UndoConflict means the original path exists again; nothing is
	// overwritten and the entry stays applied.
	UndoConflict UndoKind = "conflict"
	// UndoCannot means the entry cannot be restored; Reason says why.
	UndoCannot UndoKind = "cannot-restore"
	// UndoOutside means the scope guard refused the entry's location. The data
	// is untouched in quarantine or the trash; only the scope of this run is
	// too narrow, so it is reported apart from entries that cannot be restored.
	UndoOutside UndoKind = "outside-scope"
	// UndoDone means the entry was restored by an earlier undo run.
	UndoDone UndoKind = "already-restored"
)

// UndoStep is the plan for one manifest entry.
type UndoStep struct {
	// Index is the position of the entry in Manifest.Entries.
	Index int
	Entry session.Entry
	Kind  UndoKind
	// Description says what a restore does ("restore <path> from <stored>").
	Description string
	// Reason explains every kind but UndoRestore.
	Reason string
	// OutsideScope is set when the entry was refused by the scope guard, so
	// the caller can retry with a guard that also allows user locations.
	OutsideScope bool
}

// undoConflictError is a refusal to overwrite that is not a file conflict of
// a trasher (an existing branch, an occupied worktree path). It matches
// trash.ErrRestoreConflict so RunUndo reports every kind of conflict alike,
// while keeping the action's own message.
type undoConflictError struct{ msg string }

func (e *undoConflictError) Error() string { return e.msg }

func (e *undoConflictError) Is(target error) bool { return target == trash.ErrRestoreConflict }

// PlanUndo classifies every entry of m, last applied first. Entries are undone
// in reverse order because later steps can depend on earlier ones (a parent
// directory quarantined after its child must be back before the child is put
// into it). Manifests are user-editable files, so nothing in an entry is
// trusted: paths are checked against env.Guard here and again by the actions.
// The plan only reads the file system.
func PlanUndo(m *session.Manifest, env *Env) []UndoStep {
	steps := make([]UndoStep, 0, len(m.Entries))
	for i := len(m.Entries) - 1; i >= 0; i-- {
		steps = append(steps, planUndoEntry(env, i, m.Entries[i]))
	}
	return steps
}

func planUndoEntry(env *Env, idx int, e session.Entry) UndoStep {
	st := UndoStep{Index: idx, Entry: e, Description: describeUndo(e)}
	if kind, reason := notUndoable(e); reason != "" {
		st.Kind, st.Reason = kind, reason
		return st
	}
	if p := outsideScope(env, e); p != "" {
		st.Kind, st.OutsideScope = UndoOutside, true
		st.Reason = fmt.Sprintf("outside the current scope; re-run from %s or with --path", repoHint(p))
		return st
	}
	if reason := storedCopyGone(e); reason != "" {
		st.Kind, st.Reason = UndoCannot, reason
		return st
	}
	if e.Action == findings.ActionTrash && e.Trash != nil {
		if _, err := os.Lstat(e.Trash.OriginalPath); err == nil {
			st.Kind, st.Reason = UndoConflict, "original path already exists; it is never overwritten"
			return st
		}
	}
	st.Kind = UndoRestore
	return st
}

// notUndoable returns the reason an entry can never be handed to Undo,
// independent of the current state of the file system.
func notUndoable(e session.Entry) (UndoKind, string) {
	switch {
	case e.Status == session.StatusRestored:
		return UndoDone, "already restored"
	case e.Status != session.StatusApplied:
		reason := "the entry was " + string(e.Status) + ", nothing was changed"
		if e.Error != "" {
			reason += " (" + e.Error + ")"
		}
		return UndoCannot, reason
	case e.Trash != nil && e.Trash.Strategy == config.StrategyDelete:
		return UndoCannot, "permanently deleted (the delete strategy keeps no copy)"
	case isMaintenance(e.Action):
		return UndoCannot, "git maintenance cannot be undone"
	case !e.Restorable:
		return UndoCannot, "not restorable"
	}
	if _, ok := Get(e.Action); !ok {
		return UndoCannot, fmt.Sprintf("unknown action type %q (written by a different brooom version?)", e.Action)
	}
	return "", ""
}

func isMaintenance(t findings.ActionType) bool {
	switch t {
	case findings.ActionGitGC, findings.ActionGitPrune, findings.ActionGitReflogExpire, findings.ActionPruneWorktrees:
		return true
	}
	return false
}

// storedCopyGone reports a trashed entry whose stored copy no longer exists.
// Only "does not exist" counts: a copy that cannot be inspected (macOS
// denying access to the Trash) is left to the trasher, which explains it.
func storedCopyGone(e session.Entry) string {
	if e.Trash == nil || e.Trash.StoredPath == "" {
		return ""
	}
	if _, err := os.Lstat(e.Trash.StoredPath); errors.Is(err, os.ErrNotExist) {
		return fmt.Sprintf("the stored copy %s is gone", e.Trash.StoredPath)
	}
	return ""
}

// scopedPath is a path of an entry that undo would write to.
type scopedPath struct {
	path string
	// parentOnly keeps the final element unresolved (it may not exist yet).
	parentOnly bool
}

// entryPaths lists the locations undoing e touches, in the spelling the
// action will use.
func entryPaths(e session.Entry) []scopedPath {
	var out []scopedPath
	if e.Trash != nil && e.Trash.OriginalPath != "" {
		out = append(out, scopedPath{e.Trash.OriginalPath, true})
	}
	if wt := e.Undo["worktree"]; wt != "" {
		out = append(out, scopedPath{wt, true})
		if repo := e.Undo["repo"]; repo != "" {
			out = append(out, scopedPath{repo, false})
		}
	}
	if len(out) == 0 && e.Path != "" {
		out = append(out, scopedPath{e.Path, false})
	}
	return out
}

// outsideScope returns the first path of e that the guard refuses, or "". A
// missing guard refuses everything.
func outsideScope(env *Env, e session.Entry) string {
	for _, sp := range entryPaths(e) {
		if env.Guard == nil {
			return sp.path
		}
		var err error
		if sp.parentOnly {
			_, err = env.Guard.ResolveParent(sp.path)
		} else {
			_, err = env.Guard.Resolve(sp.path)
		}
		if errors.Is(err, scope.ErrOutsideScope) {
			return sp.path
		}
	}
	return ""
}

// repoHint names the repository around path for the scope message, walking
// up to the nearest existing directory because the path itself may be gone.
func repoHint(path string) string {
	for dir := path; ; dir = filepath.Dir(dir) {
		if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
			if root, err := scope.FindRepoRoot(dir); err == nil {
				return root
			}
			return "the repository it belongs to"
		}
		if filepath.Dir(dir) == dir {
			return "the repository it belongs to"
		}
	}
}

// describeUndo is the one-line description of a restore.
func describeUndo(e session.Entry) string {
	from := ""
	if e.Trash != nil {
		from = " from trash"
		if e.Trash.StoredPath != "" {
			from = " from " + e.Trash.StoredPath
		}
	}
	switch {
	case e.Undo["worktree"] != "":
		return "re-add worktree " + e.Undo["worktree"] + from
	case e.Action == findings.ActionDeleteBranch && e.Undo["branch"] != "":
		return fmt.Sprintf("recreate branch %s at %s (%s)", e.Undo["branch"], shortSHA(e.Undo["sha"]), e.Path)
	case e.Trash != nil:
		return "restore " + e.Trash.OriginalPath + from
	}
	return fmt.Sprintf("undo %s %s", e.Action, e.Path)
}

func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
