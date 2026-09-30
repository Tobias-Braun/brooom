package cli

import (
	"io"
	"os"
	"time"

	"golang.org/x/term"

	"github.com/Tobias-Braun/brooom/internal/output"
)

// stdoutTTY returns the file behind w when it is an interactive terminal.
// Writers that are not an *os.File (tests, buffers) are never terminals, so
// captured output is never colored or truncated by accident.
func stdoutTTY(w io.Writer) (*os.File, bool) {
	f, ok := w.(*os.File)
	if !ok || !output.IsTerminal(f) {
		return nil, false
	}
	return f, true
}

// terminalWidth returns the terminal columns of w, or 0 when w is not a
// terminal or the size is unknown. 0 means "never truncate", which keeps
// piped output complete.
func terminalWidth(w io.Writer) int {
	f, ok := stdoutTTY(w)
	if !ok {
		return 0
	}
	cols, _, err := term.GetSize(int(f.Fd()))
	if err != nil || cols < 0 {
		return 0
	}
	return cols
}

// colorInputs are the facts resolveColor decides from, gathered by the
// caller so the decision itself stays a pure, table-testable function.
type colorInputs struct {
	// noColorFlag is the --no-color flag.
	noColorFlag bool
	// configColor is config output.color: auto, always or never. Unknown
	// values are rejected by config validation and treated as auto here.
	configColor string
	// noColorEnv is the value of the NO_COLOR environment variable.
	noColorEnv string
	// isTTY is true when stdout is a terminal.
	isTTY bool
	// term is the TERM environment variable.
	term string
}

// resolveColor decides whether output is colored. Precedence: --no-color,
// then config "never", then config "always", then a non-empty NO_COLOR, then
// terminal detection (a terminal whose TERM is not "dumb"). An explicit
// config value is more specific than the global NO_COLOR preference, but
// the per-invocation flag beats everything.
func resolveColor(in colorInputs) bool {
	switch {
	case in.noColorFlag:
		return false
	case in.configColor == "never":
		return false
	case in.configColor == "always":
		return true
	case in.noColorEnv != "":
		return false
	}
	return in.isTTY && in.term != "dumb"
}

// colorEnabled gathers the inputs for resolveColor from the environment and
// the writer. On Windows consoles virtual terminal processing must be
// enabled before escapes render; when that fails color stays off. Redirected
// output never needs it.
func colorEnabled(w io.Writer, noColorFlag bool, configColor string) bool {
	f, isTTY := stdoutTTY(w)
	on := resolveColor(colorInputs{
		noColorFlag: noColorFlag,
		configColor: configColor,
		noColorEnv:  os.Getenv("NO_COLOR"),
		isTTY:       isTTY,
		term:        os.Getenv("TERM"),
	})
	// mintty interprets escapes itself and has no console mode to switch, so
	// enabling virtual terminal processing would fail and wrongly drop colour.
	if on && isTTY && !output.IsMSYSPty(f) && !enableVirtualTerminal(f) {
		return false
	}
	return on
}

// canPrompt reports whether the user can be asked a question: stdin must be a
// terminal. Scripted readers (tests, pipes) are never terminals unless a test
// injects stdinTTY.
func (a *app) canPrompt() bool {
	if a.stdinTTY != nil {
		return a.stdinTTY()
	}
	f, ok := a.io.In.(*os.File)
	return ok && output.IsTerminal(f)
}

// now is the current time, injectable for tests.
func (a *app) now() time.Time {
	if a.clock != nil {
		return a.clock()
	}
	return time.Now()
}
