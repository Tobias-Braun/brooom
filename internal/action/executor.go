package action

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/output"
	"github.com/Tobias-Braun/brooom/internal/session"
)

// ErrConfirmationRequired is returned by Run when it would have to ask for
// confirmation but stdin is not a terminal and --yes was not given. It is
// returned before anything is created or changed.
var ErrConfirmationRequired = errors.New("refusing to apply without confirmation: stdin is not a terminal; pass --yes")

// ErrInterrupted is returned by Run when the context was cancelled (Ctrl-C)
// between steps. The manifest and summary are complete for what did run.
var ErrInterrupted = fmt.Errorf("interrupted: %w", context.Canceled)

// IO are the streams the executor talks to. It is defined here so the
// package does not depend on the CLI's own IO type.
type IO struct {
	In  io.Reader
	Out io.Writer
	Err io.Writer
}

// Options configure an Executor.
type Options struct {
	// Apply executes the plan; false prints a dry run and changes nothing.
	Apply bool
	// Yes skips the confirmation prompts.
	Yes bool
	// Force allows acting on findings with overridable blocking risk flags.
	Force bool
	// Quiet drops the plan detail, totals, hint and empty-state text of a
	// dry run and shrinks the apply summary to what a script needs.
	Quiet bool
	IO    IO
	// Store receives the session manifest (required with Apply).
	Store *session.Store
	// Env is passed to every action; nil means an empty Env.
	Env *Env
	// Command is the command line stored in the manifest.
	Command string
	// SessionID is optional; callers that build a quarantine trasher need the
	// id before the run. Generated with session.NewID when empty.
	SessionID string
	// Workspaces is stored in the manifest (see session.Manifest.Workspaces).
	Workspaces bool
	// UndoFlags are the pre-quoted scope flags (--workspaces, --root, --config)
	// appended to the printed undo command, so the hint works from anywhere.
	UndoFlags []string
	// RerunHint completes the dry-run hint, e.g. "brooom branches --apply".
	// Default: "re-run with --apply".
	RerunHint string
	// Lookup resolves action implementations (default Get). Tests inject
	// fakes because the global registry panics on duplicate registration.
	Lookup func(findings.ActionType) (Action, bool)
	// Now is the clock (default time.Now).
	Now func() time.Time
	// StdinIsTTY reports whether prompting is possible (default: IO.In is a
	// terminal *os.File).
	StdinIsTTY func() bool
}

// Plan is the validated set of steps for a run.
type Plan struct {
	// Groups are ordered by detector, action priority, action type; items
	// inside a group by path and ref.
	Groups []Group
	// Skipped findings that will not be acted on, with the reason.
	Skipped []Skip
	// Failed are findings whose Action.Plan failed for another reason than
	// ErrSkipped; they never abort the run.
	Failed []Skip
	// FlaggedOnly counts findings without a suggested action.
	FlaggedOnly int
}

// Group is the set of items of one detector and action type; confirmation is
// asked per group.
type Group struct {
	Detector string
	Action   findings.ActionType
	Items    []Item
}

// Item is one planned step and whether the user confirmed it.
type Item struct {
	Step      Step
	Confirmed bool
}

// Skip is a finding that was not acted on.
type Skip struct {
	Finding findings.Finding
	Reason  string
}

// ReclaimableBytes sums the group's sizes, counting nested paths once.
func (g Group) ReclaimableBytes() int64 {
	return findings.Reclaimable(g.findings())
}

func (g Group) findings() []findings.Finding {
	fs := make([]findings.Finding, 0, len(g.Items))
	for _, it := range g.Items {
		fs = append(fs, it.Step.Finding)
	}
	return fs
}

// ReclaimableBytes sums all groups, counting nested paths once.
func (p *Plan) ReclaimableBytes() int64 {
	var fs []findings.Finding
	for _, g := range p.Groups {
		fs = append(fs, g.findings()...)
	}
	return findings.Reclaimable(fs)
}

// Empty reports whether there is nothing to execute.
func (p *Plan) Empty() bool { return len(p.Groups) == 0 }

// Result is the outcome of Run.
type Result struct {
	// SessionID is empty for dry runs and when nothing was confirmed.
	SessionID string
	Applied   int
	Skipped   int
	Failed    int
	// ReclaimedBytes sums the applied entries.
	ReclaimedBytes int64
	// Entries are the recorded (applied and failed) entries, as in the manifest.
	Entries []session.Entry
	// Skips are all skipped findings: plan-time, declined, re-plan, interrupted.
	Skips []Skip
	// Failures are failed steps (with Error set) plus plan-time failures.
	Failures []session.Entry
	// Plan is the plan that was shown (and confirmed).
	Plan *Plan
	// UndoFlags are the scope flags the printed undo command repeats.
	UndoFlags []string
}

// Restorable reports whether any applied entry can be undone.
func (r *Result) Restorable() bool {
	for _, e := range r.Entries {
		if e.Status == session.StatusApplied && e.Restorable {
			return true
		}
	}
	return false
}

// Executor is the single place where Brooom changes anything. A run has
// three phases. Plan validates every finding through Action.Plan and only
// reads. Confirmation collects all answers up front, so quitting halfway
// changes nothing. Execution then re-plans each step immediately before
// applying it, because the plan may be minutes old by then (the user was
// reading prompts) and the world may have moved on: a file opened, a branch
// that got a commit. The first Plan decides what to offer; the second one
// decides whether it is still safe.
type Executor struct {
	opts Options
	env  *Env
}

// NewExecutor returns an Executor with defaults filled in.
func NewExecutor(o Options) *Executor {
	if o.Lookup == nil {
		o.Lookup = Get
	}
	if o.Now == nil {
		o.Now = time.Now
	}
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
	// Copy the Env so folding Options.Force in never mutates the caller's.
	env := Env{}
	if o.Env != nil {
		env = *o.Env
	}
	env.Force = env.Force || o.Force
	return &Executor{opts: o, env: &env}
}

// Run plans and, with Apply, confirms and executes. Without Apply it prints
// the plan as a dry run and creates nothing.
func (e *Executor) Run(ctx context.Context, fs []findings.Finding) (*Result, error) {
	if e.opts.Apply && !e.opts.Yes && !e.opts.StdinIsTTY() {
		return nil, ErrConfirmationRequired
	}
	if e.opts.Apply && e.opts.Store == nil {
		return nil, errors.New("apply requires a session store")
	}
	plan := e.Plan(ctx, fs)
	res := planResult(plan)
	// An interrupted plan is incomplete, so it is neither shown nor offered
	// for confirmation.
	if ctx.Err() != nil {
		return res, ErrInterrupted
	}
	out := e.opts.IO.Out
	if !e.opts.Quiet {
		renderPlan(out, plan)
	}
	if plan.Empty() {
		if !e.opts.Quiet {
			fmt.Fprintln(out, "nothing to clean")
		}
		return res, nil
	}
	if !e.opts.Apply {
		if e.opts.Quiet {
			return res, nil
		}
		fmt.Fprintf(out, "dry run: nothing was changed; %s to execute\n", output.Sanitize(e.opts.RerunHint))
		return res, nil
	}
	return e.apply(ctx, plan, res)
}

// planResult seeds a Result with the skips and failures found while planning.
func planResult(plan *Plan) *Result {
	res := &Result{Plan: plan}
	res.Skips = append(res.Skips, plan.Skipped...)
	for _, s := range plan.Failed {
		res.Failures = append(res.Failures, failedEntry(s))
	}
	res.Skipped, res.Failed = len(res.Skips), len(res.Failures)
	return res
}

// failedEntry renders a plan-time failure in the shape of a failed entry.
func failedEntry(s Skip) session.Entry {
	f := s.Finding
	return session.Entry{
		FindingID: f.ID, Detector: f.Detector, Action: f.SuggestedAction.Type,
		Path: f.Path, Ref: f.Ref, SizeBytes: f.SizeBytes,
		Status: session.StatusFailed, Error: s.Reason,
	}
}

// apply confirms and executes a non-empty plan.
func (e *Executor) apply(ctx context.Context, plan *Plan, res *Result) (*Result, error) {
	e.warnBeforeDelete(plan)
	if e.opts.Yes {
		plan.setConfirmed(true)
	} else {
		newConfirmer(e.opts.IO.In, e.opts.IO.Out).confirm(plan)
	}
	var confirmed []Item
	planSkips := len(res.Skips)
	for _, g := range plan.Groups {
		for _, it := range g.Items {
			if it.Confirmed {
				confirmed = append(confirmed, it)
				continue
			}
			res.Skips = append(res.Skips, Skip{it.Step.Finding, "not confirmed"})
		}
	}
	res.Skipped = len(res.Skips)
	if len(confirmed) == 0 {
		fmt.Fprintln(e.opts.IO.Out, "nothing confirmed; nothing was changed")
		return res, nil
	}
	if ctx.Err() != nil {
		return res, ErrInterrupted
	}
	return e.execute(ctx, confirmed, res, planSkips)
}

// runState carries the mutable state of the execution phase.
type runState struct {
	e   *Executor
	m   *session.Manifest
	res *Result
}

// execute creates the manifest, runs the confirmed steps in order and
// prints the summary. The manifest exists on disk before the first step, so
// a crash mid-run still leaves a record of what was already done.
func (e *Executor) execute(ctx context.Context, items []Item, res *Result, planSkips int) (*Result, error) {
	now := e.opts.Now()
	id := e.opts.SessionID
	if id == "" {
		id = session.NewID(now)
	}
	m := &session.Manifest{Version: session.ManifestVersion, ID: id, StartedAt: now.UTC(), Command: e.opts.Command, Workspaces: e.opts.Workspaces, Entries: []session.Entry{}}
	if err := e.opts.Store.Save(m); err != nil {
		return res, fmt.Errorf("create session manifest (nothing was changed): %w", err)
	}
	res.SessionID = id
	res.UndoFlags = e.opts.UndoFlags
	e.env.plannedDeletes = plannedBranchDeletes(items)
	rs := &runState{e: e, m: m, res: res}
	// The live tracked-files check of the re-plan and of Apply is answered
	// once per repository for the whole run (taken now, after confirmation)
	// instead of once per item; a failure leaves every item unknown.
	runErr := rs.loop(e.batchTrackedForItems(ctx, items), items)
	// An interruption still finishes the manifest; only a failed save skips it.
	if runErr == nil || errors.Is(runErr, ErrInterrupted) {
		m.Finish(e.opts.Now())
		if err := e.opts.Store.Save(m); err != nil {
			runErr = fmt.Errorf("finish session manifest %s: %w", id, err)
		}
	}
	res.ReclaimedBytes = m.ReclaimedBytes
	res.Skipped = len(res.Skips)
	renderSummary(e.opts.IO.Out, res, res.Skips[planSkips:], e.opts.Quiet)
	return res, runErr
}

// loop runs the items in plan order. Cancellation is checked between steps
// only and yields ErrInterrupted; any other error means the manifest could
// not be saved.
func (rs *runState) loop(ctx context.Context, items []Item) error {
	for i, it := range items {
		if ctx.Err() != nil {
			rs.skipRest(items[i:], "interrupted")
			return ErrInterrupted
		}
		if err := rs.runItem(ctx, it.Step); err != nil {
			rs.skipRest(items[i+1:], "not run: session manifest could not be saved")
			return err
		}
	}
	return nil
}

func (rs *runState) skipRest(items []Item, reason string) {
	for _, it := range items {
		rs.res.Skips = append(rs.res.Skips, Skip{it.Step.Finding, reason})
	}
}

// runItem re-plans and applies one step, then records the entry. Only a
// failing manifest Save is returned as an error; a failing action is just
// a failed entry.
func (rs *runState) runItem(ctx context.Context, planned Step) error {
	f := planned.Finding
	act, ok := rs.e.opts.Lookup(f.SuggestedAction.Type)
	if !ok {
		rs.res.Skips = append(rs.res.Skips, Skip{f, "action not available"})
		return nil
	}
	// An uncancellable context for the re-plan as well as the apply: a
	// cancelled re-plan would fail its git calls and record a bogus failed
	// entry. The loop stops between steps instead, so a step that started
	// is never interrupted half way (a trash move or branch delete).
	ctx = context.WithoutCancel(ctx)
	step, err := act.Plan(ctx, rs.e.env, f)
	if err != nil {
		return rs.replanFailed(f, err)
	}
	entry, applyErr := act.Apply(ctx, rs.e.env, step)
	if entry.Status == session.StatusSkipped {
		rs.res.Skips = append(rs.res.Skips, Skip{f, entry.Error})
		return nil
	}
	rs.e.fill(&entry, step, applyErr)
	return rs.record(entry)
}

// replanFailed handles an error from the second Plan: ErrSkipped skips the
// step, anything else is recorded as a failed entry.
func (rs *runState) replanFailed(f findings.Finding, err error) error {
	if errors.Is(err, ErrSkipped) {
		rs.res.Skips = append(rs.res.Skips, Skip{f, skipReason(err)})
		return nil
	}
	entry := failedEntry(Skip{f, err.Error()})
	entry.At = rs.e.opts.Now().UTC()
	return rs.record(entry)
}

// fill completes fields the action left empty; values the action set are
// never overridden.
func (e *Executor) fill(en *session.Entry, s Step, applyErr error) {
	f := s.Finding
	setIfEmpty(&en.FindingID, f.ID)
	setIfEmpty(&en.Detector, f.Detector)
	setIfEmpty(&en.Action, f.SuggestedAction.Type)
	setIfEmpty(&en.Path, f.Path)
	setIfEmpty(&en.Ref, f.Ref)
	setIfEmpty(&en.SizeBytes, f.SizeBytes)
	if en.At.IsZero() {
		en.At = e.opts.Now().UTC()
	}
	if applyErr != nil {
		en.Status = session.StatusFailed
		setIfEmpty(&en.Error, applyErr.Error())
	}
	setIfEmpty(&en.Status, session.StatusApplied)
}

// setIfEmpty assigns v when *dst is the zero value.
func setIfEmpty[T comparable](dst *T, v T) {
	var zero T
	if *dst == zero {
		*dst = v
	}
}

// record adds the entry to the manifest and result and saves. On a failing
// Save the recovery hint of the step is printed, because the manifest no
// longer describes it.
func (rs *runState) record(en session.Entry) error {
	rs.m.Add(en)
	rs.res.Entries = append(rs.res.Entries, en)
	switch en.Status {
	case session.StatusFailed:
		rs.res.Failed++
		rs.res.Failures = append(rs.res.Failures, en)
	default:
		rs.res.Applied++
	}
	// Only the new entry is appended (fsynced) to the session journal; the
	// full manifest is rewritten once at Finish. Rewriting it here made large
	// runs quadratic in I/O.
	if err := rs.e.opts.Store.AppendEntry(rs.m.ID, len(rs.m.Entries)-1, en); err != nil {
		if en.RecoveryHint != "" {
			fmt.Fprintf(rs.e.opts.IO.Err, "recovery hint for %s: %s\n", entryLabel(en.Path, en.Ref), output.Sanitize(en.RecoveryHint))
		}
		return fmt.Errorf("save session manifest %s after %s: %w", rs.m.ID, entryLabel(en.Path, en.Ref), err)
	}
	return nil
}

// warnBeforeDelete shows the one-time permanence warning of the delete
// strategy before anything asks for confirmation: a warning that only appears
// once the user has said yes cannot inform that decision. It runs in apply
// only, so dry runs (whose output may be piped away) never consume it, and the
// trash action still calls BeforeDelete right before a removal as a backstop
// for steps that reach it without a plan.
func (e *Executor) warnBeforeDelete(plan *Plan) {
	env := e.opts.Env
	if env == nil || env.BeforeDelete == nil || env.Trasher == nil {
		return
	}
	for _, g := range plan.Groups {
		if g.Action != findings.ActionTrash && g.Action != findings.ActionRemoveWorktree {
			continue
		}
		// An error here surfaces again when the step itself is applied.
		if tr, err := env.Trasher(g.Detector); err == nil && tr.Strategy() == config.StrategyDelete {
			env.BeforeDelete()
			// Returning after the first delete-strategy group is intended:
			// the permanence warning is shown once per run, not per group.
			return
		}
	}
}

// plannedBranchDeletes collects the delete-branch steps of a run, so hints can
// tell which refs are going away in the same session.
func plannedBranchDeletes(items []Item) map[string]struct{} {
	out := map[string]struct{}{}
	for _, it := range items {
		f := it.Step.Finding
		if f.SuggestedAction.Type == findings.ActionDeleteBranch {
			out[plannedKey(f.Path, "refs/heads/"+f.Ref)] = struct{}{}
		}
	}
	return out
}
