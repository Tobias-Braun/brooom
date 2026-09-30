package progressui

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/Tobias-Braun/brooom/internal/progress"
)

// update rewrites the golden files: go test ./internal/cli/progressui -update.
var update = flag.Bool("update", false, "rewrite the golden files")

// plainModel is a model without colours (an Ascii renderer), driven headlessly:
// no program, no terminal, the state is set directly.
func plainModel(width int) Model {
	m := NewModel(lipgloss.NewRenderer(&bytes.Buffer{}), termenv.Ascii, nil)
	next, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: 24})
	return next.(Model)
}

// scanState is a scan halfway through a workspace.
func scanState() State {
	var s State
	s.StartPhase(progress.PhaseDiscover, 0)
	s.StartPhase(progress.PhaseScan, 24)
	for range 8 {
		s.AddStep("stale-branch on /work/api")
	}
	for range 5 {
		s.AddFinding("stale-branch", "/work/api")
	}
	for range 3 {
		s.AddFinding("build-artifacts", "/work/web")
	}
	s.AddFinding("worktrees", "/work/api")
	return s
}

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update to create it)", err)
	}
	if string(want) != got {
		t.Errorf("%s differs\n got:\n%s\nwant:\n%s", name, got, want)
	}
}

func TestViewGolden(t *testing.T) {
	long := scanState()
	for i, d := range []string{"ai-artifacts", "git-bloat", "large-untracked", "log-and-runtime-files", "merged-branch"} {
		long.AddFinding(d, "/work/repo"+string(rune('a'+i)))
	}
	long.Current = "build-artifacts on /very/long/path/that/keeps/going/and/going/and/going/beyond/the/terminal/width"

	apply := scanState()
	apply.StartPhase(progress.PhaseApply, 5)
	apply.AddStep("/work/web/dist")
	apply.Bytes = 1_250_000_000

	discover := State{}
	discover.StartPhase(progress.PhaseDiscover, 0)

	unknownTotal := State{}
	unknownTotal.StartPhase(progress.PhasePlan, 0)
	unknownTotal.AddStep("/work/x")

	hostile := State{}
	hostile.StartPhase(progress.PhaseApply, 2)
	hostile.AddStep("evil\x1b[2J\nname")

	done := apply
	done.Mode = ModeDone
	failed := scanState()
	failed.Mode = ModeFailed
	hidden := scanState()
	hidden.Mode = ModeHidden

	tests := []struct {
		name  string
		state State
		width int
	}{
		{"scan_midway", scanState(), 80},
		{"scan_many_detectors_narrow", long, 50},
		{"apply_with_bytes", apply, 80},
		{"discover_unknown_total", discover, 80},
		{"plan_count_only", unknownTotal, 80},
		{"hostile_label_sanitized", hostile, 80},
		{"summary_done", done, 80},
		{"summary_failed", failed, 80},
		{"hidden", hidden, 80},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := plainModel(tc.width)
			m.SetState(tc.state)
			view := m.View()
			golden(t, tc.name, view)
			for i, line := range strings.Split(strings.TrimSuffix(view, "\n"), "\n") {
				if w := lipgloss.Width(line); w > tc.width {
					t.Errorf("line %d is %d cells wide, terminal is %d: %q", i, w, tc.width, line)
				}
			}
		})
	}
}

func TestViewHasNoControlCharacters(t *testing.T) {
	m := plainModel(80)
	var s State
	s.StartPhase(progress.PhaseScan, 3)
	s.AddStep("evil\x1b[2J\rname")
	s.AddFinding("d\x1b[31m", "/tmp/\x1b]0;title\x07x")
	m.SetState(s)
	if strings.ContainsAny(m.View(), "\x1b\r\x07") {
		t.Errorf("untrusted text reached the terminal unescaped: %q", m.View())
	}
}

func TestFinalViewsEndWithNewline(t *testing.T) {
	// bubbletea erases the cursor's line when the program stops, so a final
	// view must leave the cursor on an empty line below the summary.
	for _, mode := range []Mode{ModeDone, ModeFailed} {
		m := plainModel(80)
		s := scanState()
		s.Mode = mode
		m.SetState(s)
		if v := m.View(); !strings.HasSuffix(v, "\n") || strings.Count(v, "\n") != 1 {
			t.Errorf("mode %d view %q is not exactly one line plus newline", mode, v)
		}
	}
}

func TestColoursFollowTheProfile(t *testing.T) {
	colour := NewModel(func() *lipgloss.Renderer {
		r := lipgloss.NewRenderer(&bytes.Buffer{})
		r.SetColorProfile(termenv.TrueColor)
		return r
	}(), termenv.TrueColor, nil)
	colour.SetState(scanState())
	if !strings.Contains(colour.View(), "\x1b[") {
		t.Error("a TrueColor profile produced no escape sequences")
	}
	plain := plainModel(80)
	plain.SetState(scanState())
	if strings.Contains(plain.View(), "\x1b") {
		t.Errorf("an Ascii profile produced escape sequences: %q", plain.View())
	}
}

func TestStateCountsAndClone(t *testing.T) {
	s := scanState()
	if s.Findings != 9 || s.Detectors["stale-branch"] != 5 || s.Targets["/work/api"] != 6 {
		t.Fatalf("counts: %+v", s)
	}
	c := s.Clone()
	s.AddFinding("stale-branch", "/work/api")
	if c.Findings != 9 || c.Detectors["stale-branch"] != 5 {
		t.Error("a clone shares its maps with the original")
	}
	s.StartPhase(progress.PhasePlan, 3)
	if s.Findings != 10 || s.Done != 0 || s.Total != 3 {
		t.Errorf("a new phase must restart the counters and keep the findings: %+v", s)
	}
	s.StartPhase(progress.PhasePlan, 3)
	if n := len(s.Visited); n != 3 {
		t.Errorf("visited = %v, a repeated phase must not be listed twice", s.Visited)
	}
}

func TestByCountOrdersByCountThenName(t *testing.T) {
	got := byCount(map[string]int{"b": 2, "a": 2, "c": 5})
	if got[0].name != "c" || got[1].name != "a" || got[2].name != "b" {
		t.Errorf("order = %v", got)
	}
}
