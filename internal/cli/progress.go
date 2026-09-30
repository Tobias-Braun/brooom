package cli

import (
	"fmt"
	"os"

	"github.com/Tobias-Braun/brooom/internal/cli/progressui"
	"github.com/Tobias-Braun/brooom/internal/output"
	"github.com/Tobias-Braun/brooom/internal/progress"
)

// The values of --progress.
const (
	progressAuto   = "auto"
	progressAlways = "always"
	progressNever  = "never"
)

// progressModes lists the accepted values, for messages and completion.
var progressModes = []string{progressAuto, progressAlways, progressNever}

// parseProgressMode validates the --progress value.
func parseProgressMode(v string) (string, error) {
	switch v {
	case progressAuto, progressAlways, progressNever:
		return v, nil
	}
	return "", usageError{fmt.Errorf("invalid --progress %q (want auto, always or never)", v)}
}

// progressInputs are the facts showProgress decides from, gathered by the
// caller so the decision is a pure, table-testable function like resolveColor.
type progressInputs struct {
	// mode is the --progress flag.
	mode string
	// format is the resolved output format of the command.
	format string
	// stderrTTY reports whether stderr is an interactive terminal.
	stderrTTY bool
	// quiet is --quiet, verbose is --verbose.
	quiet, verbose bool
	// ci is the CI environment variable; term is TERM.
	ci, term string
}

// showProgress decides whether the live display runs.
//
// The machine formats never show it, whatever the mode: their consumers may
// merge stderr into the stream they parse, and json, ndjson and plain must
// stay byte-identical to a run without a display. Beyond that, never is
// never, and always draws even without a terminal (an explicit request, for
// example to capture the display in a test). Auto is the conservative
// default and needs a terminal that can redraw in place: stderr must be a
// TTY, TERM must not be dumb, and neither --quiet nor CI may be set. CI
// counts when set to anything (CI= is not a CI), as most tools treat it.
// --verbose also switches auto off: its diagnostics are log lines on the same
// stderr, and a live region redrawn in place would be shredded by them.
//
// NO_COLOR is deliberately absent: it removes colour only, the display
// itself stays.
func showProgress(in progressInputs) bool {
	switch {
	case machineFormats[in.format], in.mode == progressNever:
		return false
	case in.mode == progressAlways:
		return true
	}
	return in.stderrTTY && !in.quiet && !in.verbose && in.ci == "" && in.term != "dumb"
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
// called, and for a rejected --progress value, the reporter stays the no-op,
// so a code path that forgets it fails closed instead of drawing.
func (a *app) useProgress(format string) {
	if a.progressDecided {
		return
	}
	a.progressDecided = true
	mode, err := parseProgressMode(a.flags.progress)
	if err != nil {
		return
	}
	if !showProgress(progressInputs{
		mode:      mode,
		format:    format,
		stderrTTY: a.stderrIsTerminal(),
		quiet:     a.flags.quiet,
		verbose:   a.flags.verbose,
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

// stopProgress ends the display, leaving its summary line. It runs once at
// the end of every invocation (also after errors and Ctrl-C) before any error
// text is printed, so the terminal is always restored and the summary comes
// before the message that explains a failure.
func (a *app) stopProgress(ok bool) {
	if a.display != nil {
		a.display.Stop(ok)
	}
}
