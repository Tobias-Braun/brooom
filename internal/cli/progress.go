package cli

import (
	"os"

	"github.com/Tobias-Braun/brooom/internal/cli/progressui"
	"github.com/Tobias-Braun/brooom/internal/output"
	"github.com/Tobias-Braun/brooom/internal/progress"
)

// progressInputs are the facts showProgress decides from, gathered by the
// caller so the decision is a pure, table-testable function like resolveColor.
type progressInputs struct {
	// format is the resolved output format of the command.
	format string
	// stderrTTY reports whether stderr is an interactive terminal.
	stderrTTY bool
	// quiet is --quiet.
	quiet bool
	// ci is the CI environment variable; term is TERM.
	ci, term string
}

// showProgress decides whether the live display runs.
//
// The machine formats never show it: their consumers may merge stderr into
// the stream they parse, and json, ndjson and plain must stay byte-identical
// to a run without a display. Otherwise it needs a terminal that can redraw
// in place: stderr must be a TTY, TERM must not be dumb, and neither --quiet
// nor CI may be set. CI counts when set to anything (CI= is not a CI), as
// most tools treat it.
//
// NO_COLOR is deliberately absent: it removes colour only, the display
// itself stays.
func showProgress(in progressInputs) bool {
	return !machineFormats[in.format] && in.stderrTTY && !in.quiet && in.ci == "" && in.term != "dumb"
}

// stderrIsTerminal reports whether the display can draw on stderr: it must be
// a terminal, and on Windows the console must accept escape sequences. Test
// doubles (buffers) are never terminals unless a test injects stderrTTY.
func (a *app) stderrIsTerminal() bool {
	if a.stderrTTY != nil {
		return a.stderrTTY()
	}
	f, ok := a.io.Err.(*os.File)
	return ok && output.IsTerminal(f) && (output.IsMSYSPty(f) || enableVirtualTerminal(f))
}

// useProgress decides once per invocation, for the resolved output format of
// the command, whether the live display runs. The first call wins: a command
// that knows better than the scan it delegates to (an applying run resolves a
// human format even when the config says json) calls it first. Until it is
// called the reporter stays the no-op, so a code path that forgets it fails
// closed instead of drawing.
func (a *app) useProgress(format string) {
	if a.progressDecided {
		return
	}
	a.progressDecided = true
	if !showProgress(progressInputs{
		format:    format,
		stderrTTY: a.stderrIsTerminal(),
		quiet:     a.flags.quiet,
		ci:        os.Getenv("CI"),
		term:      os.Getenv("TERM"),
	}) {
		return
	}
	a.display = progressui.New(progressui.Options{Out: a.io.Err, NoColor: a.flags.noColor})
}

// reporter is the progress sink handed to the engine, the executor and undo:
// the live display when one was enabled, otherwise the no-op.
func (a *app) reporter() progress.Reporter {
	if a.display == nil {
		return progress.Nop{}
	}
	return a.display
}

// stopProgress ends the display, leaving a summary line only for a failed
// run. It runs once at the end of every invocation (also after errors and
// Ctrl-C) before any error text is printed, so the terminal is always restored
// and the summary comes before the message that explains a failure.
func (a *app) stopProgress(ok bool) {
	if a.display != nil {
		a.display.Stop(ok)
	}
}
