package progressui

import (
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/Tobias-Braun/brooom/internal/progress"
)

// Options configure a Display.
type Options struct {
	// Out is where the display draws, always stderr in production. Colours
	// follow its terminal capabilities.
	Out io.Writer
	// NoColor forces plain text (--no-color); NO_COLOR is honoured by the
	// colour detection of the output itself.
	NoColor bool
}

// frameRate is the redraw rate of the live display. The reporting path never
// waits for it; a modest rate keeps CPU use negligible during long scans.
const frameRate = 20

// Display is a progress.Reporter that draws a live bubbletea display on
// stderr. On Stop it is erased after a successful run, whose own output is
// the summary, and collapsed to a "stopped" line after a failed one.
//
// The bubbletea program is started on the first Phase and stopped by Pause,
// which waits until the terminal is restored, so that results on stdout and
// confirmation prompts never share the terminal with a live region. A later
// Phase starts a new program that continues from the same State. The
// program takes no input and installs no signal handler: Ctrl-C keeps
// cancelling the command's context, and prompts read stdin undisturbed.
//
// Reporter methods only update the State under a mutex; the program samples
// it while rendering. That keeps the engine's worker goroutines from ever
// waiting on the terminal and makes it impossible to lose an event while the
// program is being stopped or restarted.
type Display struct {
	opts    Options
	model   func() Model
	profile termenv.Profile

	// life serializes Phase, Pause and Stop, so at most one program owns the
	// terminal at any time; mu guards state.
	life    sync.Mutex
	prog    *tea.Program
	done    chan struct{}
	stopped bool

	mu    sync.Mutex
	state State
}

var _ progress.Reporter = (*Display)(nil)

// New returns a Display that has not drawn anything yet.
func New(opts Options) *Display {
	if opts.Out == nil {
		opts.Out = os.Stderr
	}
	r := lipgloss.NewRenderer(opts.Out)
	if opts.NoColor {
		r.SetColorProfile(termenv.Ascii)
	}
	d := &Display{opts: opts, profile: r.ColorProfile()}
	d.model = func() Model { return NewModel(r, d.profile, d.snapshot) }
	return d
}

func (d *Display) snapshot() State {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.state.Clone()
}

// Phase implements progress.Reporter. It (re)starts the live display.
func (d *Display) Phase(p progress.Phase, total int) {
	d.life.Lock()
	defer d.life.Unlock()
	if d.stopped {
		return
	}
	d.mu.Lock()
	d.state.StartPhase(p, total)
	d.mu.Unlock()
	if d.prog == nil {
		d.start()
	}
}

// Step implements progress.Reporter.
func (d *Display) Step(label string) {
	d.mu.Lock()
	d.state.AddStep(label)
	d.mu.Unlock()
}

// Finding implements progress.Reporter.
func (d *Display) Finding(detector, target string) {
	d.mu.Lock()
	d.state.AddFinding(detector, target)
	d.mu.Unlock()
}

// Reclaimed implements progress.Reporter.
func (d *Display) Reclaimed(bytes int64) {
	d.mu.Lock()
	d.state.Bytes += bytes
	d.mu.Unlock()
}

// Pause implements progress.Reporter: the live region is erased and the
// program has exited when it returns.
func (d *Display) Pause() {
	d.life.Lock()
	defer d.life.Unlock()
	d.setMode(ModeHidden)
	d.halt()
}

// Stop ends the display for good. ok is false for a failed or interrupted
// run. A successful run erases the display and prints nothing: the command's
// own output already ends with what matters (e.g. the reclaimed size). A
// failed run collapses a live display into the "stopped" line, or prints that
// line after the results when paused; one that never reported a phase prints
// nothing. Stop is idempotent and safe to call from a defer.
func (d *Display) Stop(ok bool) {
	d.life.Lock()
	defer d.life.Unlock()
	if d.stopped {
		return
	}
	d.stopped = true
	if ok {
		d.setMode(ModeHidden)
		d.halt()
		return
	}
	d.setMode(ModeFailed)
	if d.prog != nil {
		d.halt() // the final render of the program is the summary
		return
	}
	s := d.snapshot()
	if len(s.Visited) == 0 {
		return
	}
	m := d.model()
	m.SetState(s)
	fmt.Fprint(d.opts.Out, m.View())
}

func (d *Display) setMode(m Mode) {
	d.mu.Lock()
	d.state.Mode = m
	d.mu.Unlock()
}

// start launches the program in its own goroutine. Without input, signal
// handling and alt screen it only draws lines on the output, and it exits on
// Quit. A start failure (no usable output) just leaves the display empty:
// progress is cosmetic and never fails a command.
func (d *Display) start() {
	p := tea.NewProgram(d.model(),
		tea.WithInput(nil),
		tea.WithOutput(d.opts.Out),
		tea.WithoutSignalHandler(),
		// Bracketed paste would wrap text the user types ahead into markers
		// that a later confirmation prompt would then read.
		tea.WithoutBracketedPaste(),
		tea.WithFPS(frameRate),
	)
	done := make(chan struct{})
	d.prog, d.done = p, done
	go func() {
		defer close(done)
		_, _ = p.Run()
	}()
}

// halt stops the running program, if any, and waits until it has restored the
// terminal. A program that failed to start has already returned, so Quit
// cannot block: Send gives up when the program's context is cancelled.
func (d *Display) halt() {
	if d.prog == nil {
		return
	}
	d.prog.Quit()
	select {
	case <-d.done:
	case <-time.After(haltTimeout):
		// The terminal is stuck (a blocked write); do not hang the command.
		d.prog.Kill()
		<-d.done
	}
	d.prog, d.done = nil, nil
}

// haltTimeout bounds how long Pause and Stop wait for the program to exit.
const haltTimeout = 5 * time.Second
