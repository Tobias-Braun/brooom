package action

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/Tobias-Braun/brooom/internal/output"
	"github.com/Tobias-Braun/brooom/internal/progress"
	"github.com/Tobias-Braun/brooom/internal/session"
	"github.com/Tobias-Braun/brooom/internal/trash"
)

// UndoOptions configure RunUndo.
type UndoOptions struct {
	// Apply restores; false prints the plan as a dry run and changes nothing.
	Apply bool
	// Yes skips the confirmation prompt.
	Yes bool
	IO  IO
	// Store receives the manifest after every restored entry (required with
	// Apply).
	Store *session.Store
	// StdinIsTTY reports whether prompting is possible (default: IO.In is a
	// terminal). Tests inject it.
	StdinIsTTY func() bool
	// RerunHint completes "dry run: nothing was restored; ..." (default
	// "re-run with --apply").
	RerunHint string
	// Progress receives the undo phase (default: none). It is paused before
	// the plan is printed, so the prompt never shares the terminal with it.
	Progress progress.Reporter
}

// UndoProblem is an entry that could not be restored during the run.
type UndoProblem struct {
	Label    string
	Conflict bool
	Message  string
}

// UndoResult summarizes a RunUndo call.
type UndoResult struct {
	SessionID string
	Steps     []UndoStep
	// Restored counts entries restored by this run.
	Restored int
	// Conflicts counts entries whose original location was occupied, found
	// while planning or applying.
	Conflicts int
	// Failed counts restorable entries that failed for another reason.
	Failed int
	// NotRestorable counts entries that were not restorable from the start.
	NotRestorable int
	// OutsideScope counts entries the scope guard refused. They are intact
	// and restorable with a wider scope, so they are not "not restorable".
	OutsideScope int
	// AlreadyRestored counts entries restored by an earlier run.
	AlreadyRestored int
	// Declined is true when the user answered no to the prompt.
	Declined bool
	// Problems lists the conflicts and failures found while applying.
	Problems []UndoProblem
}

// Incomplete reports whether a restorable entry was not restored: the
// condition for exit code 1 after an applied undo. Entries that were
// non-restorable from the start do not count.
func (r *UndoResult) Incomplete() bool { return r.Conflicts+r.Failed > 0 }

// RunUndo plans and, with Apply, executes the undo of m. Every restorable
// entry goes through its action's Undo; on success it is marked restored and
// the manifest is saved before the next entry, so a crash keeps an accurate
// record. Conflicts and failures never overwrite anything, leave the entry
// applied and do not stop the run. A missing confirmation on a non-terminal
// stdin returns ErrConfirmationRequired before anything is touched.
func RunUndo(ctx context.Context, env *Env, m *session.Manifest, opts UndoOptions) (*UndoResult, error) {
	opts = opts.withDefaults()
	if opts.Apply && !opts.Yes && !opts.StdinIsTTY() {
		return nil, ErrConfirmationRequired
	}
	if opts.Apply && opts.Store == nil {
		return nil, errors.New("undo requires a session store")
	}
	steps := PlanUndo(m, env)
	res := &UndoResult{SessionID: m.ID, Steps: steps}
	res.tally()
	opts.Progress.Pause()
	renderUndoPlan(opts.IO.Out, m, steps)
	if !opts.Apply {
		fmt.Fprintf(opts.IO.Out, "dry run: nothing was restored; %s to restore\n", output.Sanitize(opts.RerunHint))
		return res, nil
	}
	n := res.restorable()
	if n > 0 && !opts.Yes {
		c := newConfirmer(opts.IO.In, opts.IO.Out)
		if c.ask(fmt.Sprintf("Restore %s? [y/N] ", plural(n, "item")), "yn") != ansYes {
			res.Declined = true
			fmt.Fprintln(opts.IO.Out, "aborted: nothing was restored")
			return res, nil
		}
	}
	opts.Progress.Phase(progress.PhaseUndo, n)
	err := res.apply(ctx, env, m, opts)
	opts.Progress.Pause()
	renderUndoSummary(opts.IO.Out, res)
	return res, err
}

func (o UndoOptions) withDefaults() UndoOptions {
	if o.IO.Out == nil {
		o.IO.Out = io.Discard
	}
	if o.IO.Err == nil {
		o.IO.Err = io.Discard
	}
	if o.RerunHint == "" {
		o.RerunHint = "re-run with --apply"
	}
	if o.StdinIsTTY == nil {
		in := o.IO.In
		o.StdinIsTTY = func() bool { return isTerminal(in) }
	}
	o.Progress = progress.OrNop(o.Progress)
	return o
}

// tally counts the plan-time kinds.
func (r *UndoResult) tally() {
	for _, s := range r.Steps {
		switch s.Kind {
		case UndoConflict:
			r.Conflicts++
		case UndoCannot:
			r.NotRestorable++
		case UndoOutside:
			r.OutsideScope++
		case UndoDone:
			r.AlreadyRestored++
		}
	}
}

func (r *UndoResult) restorable() int {
	n := 0
	for _, s := range r.Steps {
		if s.Kind == UndoRestore {
			n++
		}
	}
	return n
}

// apply restores the UndoRestore steps in plan order.
func (r *UndoResult) apply(ctx context.Context, env *Env, m *session.Manifest, opts UndoOptions) error {
	for _, st := range r.Steps {
		if st.Kind != UndoRestore {
			continue
		}
		if ctx.Err() != nil {
			return ErrInterrupted
		}
		err := r.restoreOne(ctx, env, m, opts, st)
		opts.Progress.Step(entryLabel(st.Entry.Path, st.Entry.Ref))
		if err != nil {
			return err
		}
	}
	return nil
}

// restoreOne undoes one entry. The returned error is fatal (the manifest
// could not be saved); a failing restore is recorded and the run continues.
func (r *UndoResult) restoreOne(ctx context.Context, env *Env, m *session.Manifest, opts UndoOptions, st UndoStep) error {
	label := entryLabel(st.Entry.Path, st.Entry.Ref)
	act, ok := Get(st.Entry.Action)
	if !ok {
		r.fail(label, "unknown action type "+string(st.Entry.Action), false)
		return nil
	}
	err := act.Undo(ctx, env, m.Entries[st.Index])
	switch {
	case errors.Is(err, trash.ErrRestoreConflict):
		r.Conflicts++
		r.fail(label, err.Error(), true)
		return nil
	case err != nil:
		r.fail(label, err.Error(), false)
		return nil
	}
	m.Entries[st.Index].Status = session.StatusRestored
	m.RecomputeReclaimed()
	r.Restored++
	if err := opts.Store.Save(m); err != nil {
		return fmt.Errorf("%s was restored, but the session manifest %s could not be saved: %w", label, m.ID, err)
	}
	return nil
}

// fail records a problem; conflicts are counted by the caller.
func (r *UndoResult) fail(label, msg string, conflict bool) {
	if !conflict {
		r.Failed++
	}
	r.Problems = append(r.Problems, UndoProblem{Label: label, Conflict: conflict, Message: msg})
}

func renderUndoPlan(w io.Writer, m *session.Manifest, steps []UndoStep) {
	fmt.Fprintf(w, "undo session %s", output.Sanitize(m.ID))
	if m.Command != "" {
		fmt.Fprintf(w, " (%s)", output.Sanitize(m.Command))
	}
	fmt.Fprintln(w)
	if len(steps) == 0 {
		fmt.Fprintln(w, "  the session has no entries")
		return
	}
	for _, s := range steps {
		renderUndoStep(w, s)
	}
}

// renderUndoStep prints one plan line, plus the manual recovery hint for
// entries that will not be restored.
func renderUndoStep(w io.Writer, s UndoStep) {
	label := entryLabel(s.Entry.Path, s.Entry.Ref)
	switch s.Kind {
	case UndoRestore:
		fmt.Fprintf(w, "  %s\n", output.Sanitize(s.Description))
		return
	case UndoConflict:
		fmt.Fprintf(w, "  conflict %s: %s\n", label, output.Sanitize(s.Reason))
	case UndoDone, UndoOutside:
		fmt.Fprintf(w, "  skip %s: %s\n", label, output.Sanitize(s.Reason))
		return
	default:
		fmt.Fprintf(w, "  cannot restore %s: %s\n", label, output.Sanitize(s.Reason))
	}
	if s.Entry.RecoveryHint != "" {
		fmt.Fprintf(w, "    recovery: %s\n", output.Sanitize(s.Entry.RecoveryHint))
	}
}

func renderUndoSummary(w io.Writer, r *UndoResult) {
	for _, p := range r.Problems {
		kind := "failed"
		if p.Conflict {
			kind = "conflict"
		}
		fmt.Fprintf(w, "  %s %s: %s\n", kind, output.Sanitize(p.Label), output.Sanitize(p.Message))
	}
	fmt.Fprintf(w, "summary: %d restored, %d conflicts, %d failed, %d not restorable, %d already restored",
		r.Restored, r.Conflicts, r.Failed, r.NotRestorable, r.AlreadyRestored)
	if r.OutsideScope > 0 {
		fmt.Fprintf(w, ", %d skipped (outside scope; re-run with --path)", r.OutsideScope)
	}
	fmt.Fprintln(w)
	fmt.Fprintf(w, "session: %s\n", output.Sanitize(r.SessionID))
}
