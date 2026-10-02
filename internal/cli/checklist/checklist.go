// Package checklist is the full-screen list behind the "e" answer of the
// sweep question: every planned item with a checkbox, all checked, so the user
// can untick what should stay before anything is changed.
//
// It knows nothing about plans or findings. The caller passes labelled items
// with their group and gets back which ones are still checked, or that the
// user aborted. Aborting always means "change nothing", exactly like answering
// no to the question.
package checklist

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"golang.org/x/term"
)

// Item is one line of the list.
type Item struct {
	// Group is the heading the item is listed under; consecutive items with
	// the same group share one heading.
	Group string
	// Label describes the item (path or branch, size).
	Label   string
	Checked bool
}

// Run shows the list on out, reads keys from in and returns the checked state
// of every item (same order as items) and whether the user confirmed. It
// returns confirmed false when the user aborted or the input ended.
func Run(in io.Reader, out io.Writer, items []Item) (checked []bool, confirmed bool, err error) {
	m := newModel(items)
	// A terminal must reach bubbletea as the *os.File itself: only then does
	// it switch the terminal to raw mode, without which keys arrive line by
	// line and Enter is never seen. A terminal cannot reach end of input
	// (ctrl+d is a key, handled as abort), so only other readers are wrapped.
	input := in
	var eof *eofReader
	if f, ok := in.(*os.File); !ok || !term.IsTerminal(int(f.Fd())) {
		eof = &eofReader{r: in}
		input = eof
	}
	p := tea.NewProgram(m, tea.WithInput(input), tea.WithOutput(out), tea.WithAltScreen())
	if eof != nil {
		eof.onEOF = func() { p.Send(inputEnded{}) }
	}
	final, err := p.Run()
	if err != nil {
		return nil, false, fmt.Errorf("checklist: %w", err)
	}
	fm := final.(model)
	return fm.checkedStates(), fm.confirmed, nil
}

// model is the bubbletea model of the list.
type model struct {
	items     []Item
	cursor    int
	height    int
	confirmed bool
}

// defaultHeight is used until the terminal reports its size.
const defaultHeight = 20

// chrome is the number of lines the header and the footer take.
const chrome = 4

func newModel(items []Item) model {
	its := make([]Item, len(items))
	copy(its, items)
	return model{items: its, height: defaultHeight}
}

func (m model) Init() tea.Cmd { return nil }

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.height = msg.Height
	case tea.KeyMsg:
		return m.key(msg.String())
	case inputEnded:
		// The keys read before the end may still be on their way to the
		// model (they travel on a different channel), so they get a moment
		// to arrive before the list gives up.
		return m, tea.Tick(inputGrace, func(time.Time) tea.Msg { return giveUp{} })
	case giveUp:
		// A closed input is never consent.
		m.confirmed = false
		return m, tea.Quit
	}
	return m, nil
}

// key handles one key press.
func (m model) key(k string) (tea.Model, tea.Cmd) {
	switch k {
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(m.items)-1 {
			m.cursor++
		}
	case " ", "x":
		if len(m.items) > 0 {
			m.items[m.cursor].Checked = !m.items[m.cursor].Checked
		}
	case "a":
		m.toggleGroup()
	case "enter":
		m.confirmed = true
		return m, tea.Quit
	case "q", "esc", "ctrl+c", "ctrl+d":
		m.confirmed = false
		return m, tea.Quit
	}
	return m, nil
}

// toggleGroup checks every item of the cursor's group, or unchecks them all
// when they already are all checked.
func (m *model) toggleGroup() {
	if len(m.items) == 0 {
		return
	}
	g := m.items[m.cursor].Group
	all := true
	for _, it := range m.items {
		if it.Group == g && !it.Checked {
			all = false
			break
		}
	}
	for i := range m.items {
		if m.items[i].Group == g {
			m.items[i].Checked = !all
		}
	}
}

func (m model) checkedStates() []bool {
	out := make([]bool, len(m.items))
	for i, it := range m.items {
		out[i] = it.Checked
	}
	return out
}

func (m model) selected() int {
	n := 0
	for _, it := range m.items {
		if it.Checked {
			n++
		}
	}
	return n
}

// View renders the header, the visible window of items (group headings
// included) and the footer.
func (m model) View() string {
	var b strings.Builder
	b.WriteString("Untick what should stay. space: toggle, a: toggle group, enter: clean the checked items, q: change nothing\n\n")
	lines, cursorLine := m.lines()
	start, end := window(len(lines), cursorLine, max(m.height-chrome, 3))
	for _, l := range lines[start:end] {
		b.WriteString(l)
		b.WriteByte('\n')
	}
	fmt.Fprintf(&b, "\n%d of %d selected", m.selected(), len(m.items))
	return b.String()
}

// lines renders every group heading and item and returns the line index of
// the cursor.
func (m model) lines() ([]string, int) {
	var out []string
	cursorLine := 0
	prev := "\x00"
	for i, it := range m.items {
		if it.Group != prev {
			out = append(out, it.Group)
			prev = it.Group
		}
		box := "[ ]"
		if it.Checked {
			box = "[x]"
		}
		pointer := "  "
		if i == m.cursor {
			pointer = "> "
			cursorLine = len(out)
		}
		out = append(out, fmt.Sprintf("%s%s %s", pointer, box, it.Label))
	}
	return out, cursorLine
}

// window returns the [start, end) range of n lines of height h that keeps the
// cursor line visible, roughly centred.
func window(n, cursor, h int) (int, int) {
	if n <= h {
		return 0, n
	}
	start := max(cursor-h/2, 0)
	if start+h > n {
		start = n - h
	}
	return start, start + h
}

// inputEnded tells the model that no more keys can come; giveUp follows it
// after inputGrace unless a key ended the list first.
type (
	inputEnded struct{}
	giveUp     struct{}
)

// inputGrace is how long keys read before the end of input may take to reach
// the model.
const inputGrace = 100 * time.Millisecond

// eofReader reports the end of the input to the program, which bubbletea
// itself does not: without it a closed stdin would leave the list open
// forever.
type eofReader struct {
	r     io.Reader
	onEOF func()
	once  sync.Once
}

func (e *eofReader) Read(p []byte) (int, error) {
	n, err := e.r.Read(p)
	if err != nil && e.onEOF != nil {
		e.once.Do(e.onEOF)
	}
	return n, err
}
