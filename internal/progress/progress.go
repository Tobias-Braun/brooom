// Package progress is the seam between the long-running loops of Brooom (the
// detection engine, the executor, undo) and whatever shows their progress.
//
// The loops only know the small Reporter interface and never learn how, or
// whether, anything is rendered: the CLI plugs in a terminal display when a
// human is watching and the no-op Nop otherwise. Keeping the interface here,
// free of any terminal library, is what lets the core packages stay
// dependency-light and testable with a plain recording fake.
package progress

// Phase names a stage of a run. The display shows it as the headline.
type Phase string

// The phases of a run, in the order a cleanup command goes through them.
const (
	// PhaseDiscover resolves the scan scope: the repository around the
	// working directory or the workspaces below the configured roots.
	PhaseDiscover Phase = "discover"
	// PhaseScan runs the detectors over the targets.
	PhaseScan Phase = "scan"
	// PhasePlan re-validates every finding for its action (read only).
	PhasePlan Phase = "plan"
	// PhaseApply executes the confirmed steps.
	PhaseApply Phase = "apply"
	// PhaseUndo restores the entries of a session.
	PhaseUndo Phase = "undo"
)

// Reporter receives progress events. Implementations must be safe for
// concurrent use: the engine reports from several worker goroutines. Every
// method must return quickly and must never block on the terminal for long,
// because the reporting loops hold no other locks but are on the hot path.
//
// A Reporter is purely observational. Nothing in a run may depend on what it
// does with an event, so a failing display can never change what is scanned,
// planned or applied.
type Reporter interface {
	// Phase starts (or resumes after Pause) a phase with total units of
	// work; total is 0 when unknown. It restarts the counters of the phase.
	Phase(p Phase, total int)
	// Step reports one finished unit of work in the current phase. label
	// says what it was (a detector and target, a path); it is untrusted text
	// and the display must sanitize it.
	Step(label string)
	// Finding reports one new unique finding of detector on target (a path).
	Finding(detector, target string)
	// Reclaimed reports bytes freed by a step that was just applied.
	Reclaimed(bytes int64)
	// Pause hides any live display and returns only once the terminal is
	// free for ordinary output (results on stdout, confirmation prompts).
	// The next Phase call shows it again.
	Pause()
}

// Nop is the Reporter that does nothing. It is the default of every loop, so
// library callers and machine-readable runs pay nothing.
type Nop struct{}

// Phase implements Reporter.
func (Nop) Phase(Phase, int) {}

// Step implements Reporter.
func (Nop) Step(string) {}

// Finding implements Reporter.
func (Nop) Finding(string, string) {}

// Reclaimed implements Reporter.
func (Nop) Reclaimed(int64) {}

// Pause implements Reporter.
func (Nop) Pause() {}

// OrNop returns r, or Nop when r is nil, so option structs can leave the
// field unset.
func OrNop(r Reporter) Reporter {
	if r == nil {
		return Nop{}
	}
	return r
}
