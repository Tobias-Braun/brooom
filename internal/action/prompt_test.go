package action

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestAsk(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		allowed string
		want    rune
		prompts int
	}{
		{"letter", "y\n", "ynq", 'y', 1},
		{"word", "yes\n", "ynq", 'y', 1},
		{"uppercase word", "QUIT\n", "ynq", 'q', 1},
		{"crlf", "n\r\n", "ynq", 'n', 1},
		{"padded", "  n  \n", "ynq", 'n', 1},
		{"empty is no", "\n", "ynq", 'n', 1},
		{"invalid re-asks", "x\nyy\ny\n", "ynq", 'y', 3},
		{"letter not allowed re-asks", "q\nn\n", "yn", 'n', 2},
		{"eof is quit", "", "ynq", 'q', 1},
		{"eof after invalid is quit", "x\n", "ynq", 'q', 2},
		{"last line without newline", "y", "ynq", 'y', 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			c := newConfirmer(strings.NewReader(tt.input), &out)
			if got := c.ask("? ", tt.allowed); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
			if n := strings.Count(out.String(), "? "); n != tt.prompts {
				t.Errorf("%d prompts, want %d:\n%s", n, tt.prompts, out.String())
			}
		})
	}
}

func TestNilInputIsEOF(t *testing.T) {
	var out bytes.Buffer
	if got := newConfirmer(nil, &out).ask("? ", "yq"); got != 'q' {
		t.Errorf("got %q", got)
	}
}

// The prompts share one reader: answers for later questions must not be
// lost in the buffer of an earlier one.
func TestSharedReaderAcrossPrompts(t *testing.T) {
	c := newConfirmer(strings.NewReader("y\nn\nq\n"), &bytes.Buffer{})
	var got []rune
	for range 3 {
		got = append(got, c.ask("? ", "ynq"))
	}
	if string(got) != "ynq" {
		t.Errorf("got %q", string(got))
	}
}

func TestIsTerminalNonFile(t *testing.T) {
	if isTerminal(strings.NewReader("")) {
		t.Error("a string reader is not a terminal")
	}
	if isTerminal(nil) {
		t.Error("nil is not a terminal")
	}
}

func TestPluralAndPromptText(t *testing.T) {
	if plural(1, "item") != "1 item" || plural(2, "item") != "2 items" || plural(0, "item") != "0 items" {
		t.Error("plural")
	}
}

// TestChooseFromTheQuestion covers the "e" answer: it is only offered with a
// selector, the selector's ticks decide what runs, and aborting, an error or
// unticking everything changes nothing.
func TestChooseFromTheQuestion(t *testing.T) {
	plan := func() *Plan {
		return &Plan{Groups: []Group{{Detector: "d", Action: "trash", Items: []Item{{}, {}, {}}}}}
	}
	untickFirst := func(p *Plan) (bool, error) {
		p.Groups[0].Items[0].Confirmed = false
		return true, nil
	}
	tests := []struct {
		name  string
		in    string
		sel   func(*Plan) (bool, error)
		ok    bool
		count int
	}{
		{"untick one", "e\n", untickFirst, true, 2},
		{"abort", "e\n", func(*Plan) (bool, error) { return false, nil }, false, 0},
		{"error", "e\n", func(*Plan) (bool, error) { return true, errors.New("no tty") }, false, 0},
		{"untick all", "e\n", func(p *Plan) (bool, error) { p.clearConfirmed(); return true, nil }, false, 0},
		{"e without selector re-asks", "e\nn\n", nil, false, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			c := newConfirmer(strings.NewReader(tt.in), &out)
			c.sel = tt.sel
			p := plan()
			if got := c.confirm(p); got != tt.ok || p.confirmedCount() != tt.count {
				t.Errorf("confirm = %v with %d confirmed, want %v with %d\n%s", got, p.confirmedCount(), tt.ok, tt.count, out.String())
			}
			if tt.sel != nil && !strings.Contains(out.String(), "[y/N/e to choose]") {
				t.Errorf("the question does not offer e:\n%s", out.String())
			}
			if tt.sel == nil && strings.Contains(out.String(), "e to choose") {
				t.Errorf("e offered without a selector:\n%s", out.String())
			}
		})
	}
}
