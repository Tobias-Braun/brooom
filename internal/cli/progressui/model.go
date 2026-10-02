package progressui

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/Tobias-Braun/brooom/internal/output"
	brooomprogress "github.com/Tobias-Braun/brooom/internal/progress"
)

// defaultWidth is used until the terminal size is known and when it cannot be
// determined (a non-terminal output with --progress=always).
const defaultWidth = 80

// Limits of the count lines: more entries are folded into "+N more" so a
// workspace scan with hundreds of repositories keeps a fixed-height display.
const (
	maxDetectorsShown = 4
	maxTargetsShown   = 3
	maxBarWidth       = 28
)

// styles are the lipgloss styles of the display, built from one renderer so
// the colour decision (terminal capability, NO_COLOR, --no-color) is made for
// stderr and not for the global default output.
type styles struct {
	phase, dim, count, fail, bytes lipgloss.Style
}

func newStyles(r *lipgloss.Renderer) styles {
	s := r.NewStyle
	return styles{
		phase: s().Bold(true).Foreground(lipgloss.Color("6")),
		dim:   s().Foreground(lipgloss.Color("8")),
		count: s().Foreground(lipgloss.Color("3")),
		fail:  s().Bold(true).Foreground(lipgloss.Color("1")),
		bytes: s().Bold(true).Foreground(lipgloss.Color("2")),
	}
}

// Model is the bubbletea model of the display. It renders a State and does
// nothing else: it takes no input (the program is started without any) and
// reads the state from source on every render, so a quit request always
// paints the final state.
type Model struct {
	styles  styles
	spinner spinner.Model
	bar     progress.Model
	width   int
	state   State
	// source, when set, returns the current state; tests leave it nil and set
	// the state with SetState instead.
	source func() State
}

// NewModel builds a model whose colours follow profile (termenv.Ascii for
// none). r renders the lipgloss styles.
func NewModel(r *lipgloss.Renderer, profile termenv.Profile, source func() State) Model {
	st := newStyles(r)
	sp := spinner.New(spinner.WithSpinner(spinner.MiniDot))
	sp.Style = st.phase
	bar := progress.New(
		progress.WithSolidFill("6"),
		progress.WithoutPercentage(),
		progress.WithColorProfile(profile),
	)
	return Model{styles: st, spinner: sp, bar: bar, width: defaultWidth, source: source}
}

// SetState replaces the rendered state (headless tests).
func (m *Model) SetState(s State) { m.state = s }

// Init starts the spinner, whose ticks also drive the refresh of the view.
func (m Model) Init() tea.Cmd { return m.spinner.Tick }

// Update reacts to the terminal size and the spinner ticks. Any message makes
// bubbletea render again, which is what picks up the newest state.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		if msg.Width > 0 {
			m.width = msg.Width
		}
		return m, nil
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	}
	return m, nil
}

// current returns the state to draw.
func (m Model) current() State {
	if m.source != nil {
		return m.source()
	}
	return m.state
}

// View renders the state: nothing while hidden, one summary line when failed,
// otherwise the live block. A final view ends with a newline because
// bubbletea erases the line the cursor is on when the program stops.
func (m Model) View() string {
	s := m.current()
	switch s.Mode {
	case ModeHidden:
		return ""
	case ModeFailed:
		return m.summary(s) + "\n"
	}
	return m.live(s)
}

// live is the running display: headline with spinner and bar, what is being
// worked on, and the counts.
func (m Model) live(s State) string {
	lines := []string{m.headline(s)}
	if s.Current != "" {
		lines = append(lines, m.styles.dim.Render("  now: "+output.Sanitize(s.Current)))
	}
	if s.Findings > 0 {
		lines = append(lines, m.detectorLine(s), m.targetLine(s))
	}
	if s.Bytes > 0 {
		lines = append(lines, "  reclaimed "+m.styles.bytes.Render(output.FormatSize(s.Bytes)))
	}
	return m.fit(lines)
}

// fit truncates every line to the terminal width, so a long path never wraps
// (a wrapped line would break the in-place redraw).
func (m Model) fit(lines []string) string {
	clip := lipgloss.NewStyle().MaxWidth(m.width)
	for i, l := range lines {
		lines[i] = clip.Render(l)
	}
	return strings.Join(lines, "\n")
}

// phaseLabel is the headline word of a phase.
func phaseLabel(p brooomprogress.Phase) string {
	switch p {
	case brooomprogress.PhaseDiscover:
		return "Discovering"
	case brooomprogress.PhaseScan:
		return "Scanning"
	case brooomprogress.PhasePlan:
		return "Planning"
	case brooomprogress.PhaseApply:
		return "Applying"
	case brooomprogress.PhaseUndo:
		return "Restoring"
	}
	return string(p)
}

// headline is "<spinner> Scanning  <bar> 8/24". Without a known total there
// is no bar, only the running count.
func (m Model) headline(s State) string {
	line := m.spinner.View() + " " + m.styles.phase.Render(phaseLabel(s.Phase))
	switch {
	case s.Total > 0:
		bar := m.bar
		bar.Width = min(maxBarWidth, max(10, m.width/3))
		pct := float64(s.Done) / float64(s.Total)
		line += "  " + bar.ViewAs(min(pct, 1)) + " " + m.styles.count.Render(fmt.Sprintf("%d/%d", s.Done, s.Total))
	case s.Done > 0:
		line += "  " + m.styles.count.Render(fmt.Sprintf("%d done", s.Done))
	}
	return line
}

// detectorLine lists the findings per detector.
func (m Model) detectorLine(s State) string {
	return "  " + m.styles.count.Render(fmt.Sprintf("%d findings", s.Findings)) + "  " +
		m.list(byCount(s.Detectors), maxDetectorsShown, func(n string) string { return n })
}

// targetLine lists the findings per target, by directory name; the full path
// is the key, so two targets named alike stay separate.
func (m Model) targetLine(s State) string {
	return "  " + m.styles.dim.Render("targets") + "  " +
		m.list(byCount(s.Targets), maxTargetsShown, filepath.Base)
}

// list renders "name n · name n · +k more" for the first limit entries.
func (m Model) list(items []counted, limit int, label func(string) string) string {
	var parts []string
	for i, it := range items {
		if i == limit {
			parts = append(parts, m.styles.dim.Render(fmt.Sprintf("+%d more", len(items)-limit)))
			break
		}
		parts = append(parts, output.Sanitize(label(it.name))+" "+m.styles.count.Render(fmt.Sprint(it.count)))
	}
	return strings.Join(parts, m.styles.dim.Render(" · "))
}

// summary is the collapsed line left on screen when a run fails.
func (m Model) summary(s State) string {
	names := make([]string, len(s.Visited))
	for i, p := range s.Visited {
		names[i] = string(p)
	}
	parts := []string{m.styles.fail.Render("✗ stopped")}
	if len(names) > 0 {
		parts = append(parts, strings.Join(names, ", "))
	}
	if s.Findings > 0 {
		parts = append(parts, fmt.Sprintf("%d findings in %d detectors", s.Findings, len(s.Detectors)))
	}
	// The reclaimed size is left out: the command prints it as its last line.
	return m.fit([]string{strings.Join(parts, m.styles.dim.Render(" · "))})
}
