package checklist

import (
	"bytes"
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func sample() []Item {
	return []Item{
		{Group: "branches", Label: "feat/a", Checked: true},
		{Group: "branches", Label: "feat/b", Checked: true},
		{Group: "worktrees", Label: "wt-1", Checked: true},
	}
}

// press feeds keys to the model and returns the final model.
func press(m model, keys ...string) model {
	for _, k := range keys {
		next, _ := m.key(k)
		m = next.(model)
	}
	return m
}

func TestToggleAndMove(t *testing.T) {
	m := press(newModel(sample()), " ", "down", "down", "x", "up", "k", "j")
	if got, want := m.checkedStates(), []bool{false, true, false}; !slices.Equal(got, want) {
		t.Errorf("checked %v, want %v", got, want)
	}
	if m.cursor != 1 {
		t.Errorf("cursor %d", m.cursor)
	}
	// Moving past either end stays put.
	m = press(m, "up", "up", "up")
	if m.cursor != 0 {
		t.Errorf("cursor %d after moving up past the top", m.cursor)
	}
}

func TestToggleGroup(t *testing.T) {
	m := press(newModel(sample()), "a")
	if got := m.checkedStates(); !slices.Equal(got, []bool{false, false, true}) {
		t.Errorf("all checked group unchecks: %v", got)
	}
	m = press(m, " ", "a")
	if got := m.checkedStates(); !slices.Equal(got, []bool{true, true, true}) {
		t.Errorf("a partly checked group checks all: %v", got)
	}
}

func TestEnterConfirmsAndQuitAborts(t *testing.T) {
	for key, want := range map[string]bool{"enter": true, "q": false, "esc": false, "ctrl+c": false, "ctrl+d": false} {
		next, cmd := newModel(sample()).key(key)
		if next.(model).confirmed != want || cmd == nil {
			t.Errorf("%s: confirmed %v, quit %v", key, next.(model).confirmed, cmd != nil)
		}
	}
}

func TestViewGroupsAndCounts(t *testing.T) {
	m := press(newModel(sample()), "down", " ")
	v := m.View()
	for _, want := range []string{"branches\n", "  [x] feat/a", "> [ ] feat/b", "worktrees\n", "  [x] wt-1", "2 of 3 selected"} {
		if !strings.Contains(v, want) {
			t.Errorf("view lacks %q:\n%s", want, v)
		}
	}
	if strings.Count(v, "branches") != 1 {
		t.Errorf("group heading repeated:\n%s", v)
	}
}

func TestWindowKeepsTheCursorVisible(t *testing.T) {
	for _, tc := range []struct{ n, cursor, h, start, end int }{
		{5, 0, 10, 0, 5},
		{100, 0, 10, 0, 10},
		{100, 50, 10, 45, 55},
		{100, 99, 10, 90, 100},
	} {
		s, e := window(tc.n, tc.cursor, tc.h)
		if s != tc.start || e != tc.end || tc.cursor < s || tc.cursor >= e {
			t.Errorf("window(%d, %d, %d) = %d, %d; want %d, %d", tc.n, tc.cursor, tc.h, s, e, tc.start, tc.end)
		}
	}
}

func TestSizeMessageSetsTheHeight(t *testing.T) {
	next, _ := newModel(sample()).Update(tea.WindowSizeMsg{Width: 80, Height: 7})
	if next.(model).height != 7 {
		t.Errorf("height %d", next.(model).height)
	}
}

// TestRunReadsKeysFromInput drives the real program with scripted input: the
// first item is unticked and enter confirms.
func TestRunReadsKeysFromInput(t *testing.T) {
	var out bytes.Buffer
	checked, ok, err := Run(strings.NewReader(" \r"), &out, sample())
	if err != nil {
		t.Fatal(err)
	}
	if !ok || !slices.Equal(checked, []bool{false, true, true}) {
		t.Errorf("confirmed %v, checked %v", ok, checked)
	}
}

// TestRunEndOfInputChangesNothing: a closed input is never consent.
func TestRunEndOfInputChangesNothing(t *testing.T) {
	_, ok, err := Run(strings.NewReader(""), &bytes.Buffer{}, sample())
	if err != nil || ok {
		t.Errorf("confirmed %v, err %v", ok, err)
	}
}
