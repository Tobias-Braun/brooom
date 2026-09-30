package output

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/findings"
)

func render(t *testing.T, format string, r *findings.Report, opts Options) string {
	t.Helper()
	f, err := Get(format)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := f.Write(&buf, r, opts); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

// checkInvariants asserts what must hold for every rendering: no escapes
// without color, identical text with and without color, no trailing space.
func checkInvariants(t *testing.T, format string, r *findings.Report, opts Options) string {
	t.Helper()
	opts.Color = false
	plain := render(t, format, r, opts)
	if strings.Contains(plain, "\x1b") {
		t.Errorf("ANSI escape in uncolored output:\n%q", plain)
	}
	opts.Color = true
	colored := render(t, format, r, opts)
	if stripANSI(colored) != plain {
		t.Errorf("colored output differs after stripping\ncolored: %q\nplain: %q", stripANSI(colored), plain)
	}
	for _, line := range strings.Split(plain, "\n") {
		if strings.TrimRight(line, " \t") != line {
			t.Errorf("trailing whitespace in %q", line)
		}
	}
	return plain
}

func TestRegistered(t *testing.T) {
	for _, n := range []string{"table", "summary"} {
		if _, err := Get(n); err != nil {
			t.Errorf("Get(%q): %v", n, err)
		}
	}
	names := Names()
	for _, n := range []string{"table", "summary"} {
		found := false
		for _, x := range names {
			found = found || x == n
		}
		if !found {
			t.Errorf("Names() lacks %q: %v", n, names)
		}
	}
}

func TestGoldens(t *testing.T) {
	tests := []struct {
		name   string
		format string
		report func() *findings.Report
		opts   Options
	}{
		{"table_color", "table", fixtureReport, Options{Color: true}},
		{"table_nocolor", "table", fixtureReport, Options{}},
		{"table_narrow", "table", fixtureReport, Options{Width: 60}},
		{"table_longpath", "table", longPathReport, Options{}},
		{"table_quiet", "table", fixtureReport, Options{Quiet: true}},
		{"table_empty", "table", emptyReport, Options{}},
		{"table_errors_only", "table", errorsOnlyReport, Options{}},
		{"summary_nocolor", "summary", fixtureReport, Options{}},
		{"summary_color", "summary", fixtureReport, Options{Color: true}},
		{"summary_empty", "summary", emptyReport, Options{}},
		{"summary_quiet", "summary", fixtureReport, Options{Quiet: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := render(t, tt.format, tt.report(), tt.opts)
			assertGolden(t, tt.name, got)
			checkInvariants(t, tt.format, tt.report(), tt.opts)
		})
	}
}

func TestTableDoesNotMutateReport(t *testing.T) {
	r := fixtureReport()
	// Reverse so the report is not pre-sorted.
	for i, j := 0, len(r.Findings)-1; i < j; i, j = i+1, j-1 {
		r.Findings[i], r.Findings[j] = r.Findings[j], r.Findings[i]
	}
	before := append([]findings.Finding(nil), r.Findings...)
	out := render(t, "table", r, Options{})
	if !reflect.DeepEqual(before, r.Findings) {
		t.Error("report findings were mutated")
	}
	if strings.Index(out, "build-artifacts") > strings.Index(out, "merged-branch") {
		t.Error("groups are not alphabetical")
	}
}

func TestTableRecomputesTotals(t *testing.T) {
	r := fixtureReport()
	r.Totals = findings.Totals{Findings: 999}
	out := render(t, "table", r, Options{})
	if strings.Contains(out, "999") {
		t.Errorf("stale Report.Totals used:\n%s", out)
	}
}

func TestTableTruncationAndFlaggedRows(t *testing.T) {
	out := render(t, "table", fixtureReport(), Options{Width: 60})
	if !strings.Contains(out, "…") {
		t.Errorf("expected ellipsis at width 60:\n%s", out)
	}
	if strings.Contains(render(t, "table", longPathReport(), Options{}), "…") {
		t.Error("width 0 must not truncate")
	}
	full := render(t, "table", fixtureReport(), Options{})
	for _, want := range []string{"  -> worktree has uncommitted changes", "  -> unpushed", "1 finding, 0 actionable, 0 B reclaimable"} {
		if !strings.Contains(full, want) {
			t.Errorf("missing %q in:\n%s", want, full)
		}
	}
}

func TestSummaryTotalIsDeduplicated(t *testing.T) {
	out := render(t, "summary", fixtureReport(), Options{})
	// node_modules/.cache is nested in node_modules, so it counts once.
	if !strings.Contains(out, "TOTAL") || !strings.Contains(out, "1.3 GB") {
		t.Errorf("unexpected summary:\n%s", out)
	}
}

func TestSingularFinding(t *testing.T) {
	r := longPathReport()
	if out := render(t, "table", r, Options{}); !strings.Contains(out, "1 finding, 1 actionable, 2.0 kB reclaimable") {
		t.Errorf("missing singular totals:\n%s", out)
	}
}

func TestNoDescriptionForUnregisteredDetector(t *testing.T) {
	out := render(t, "table", fixtureReport(), Options{})
	if !strings.Contains(out, "\nworktrees (1)\n") && !strings.HasPrefix(out, "worktrees (1)\n") {
		t.Errorf("expected bare header for unregistered detector:\n%s", out)
	}
	if !strings.Contains(out, "merged-branch - branches already merged into the base branch (2)") {
		t.Errorf("expected described header:\n%s", out)
	}
}

func TestColumnsOmittedWhenEmpty(t *testing.T) {
	out := render(t, "table", longPathReport(), Options{})
	if strings.Contains(out, "REF") || strings.Contains(out, "FLAGS") {
		t.Errorf("REF/FLAGS should be omitted:\n%s", out)
	}
}

func TestMinimumPathWidth(t *testing.T) {
	out := render(t, "table", longPathReport(), Options{Width: 10})
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "…") && !strings.HasSuffix(line, "fact.log") {
			t.Errorf("tail not preserved: %q", line)
		}
		if strings.Contains(line, "…") && len([]rune(line)) < minPathWidth {
			t.Errorf("path narrower than minimum: %q", line)
		}
	}
}

func TestQuietErrorsAndNothing(t *testing.T) {
	out := render(t, "table", errorsOnlyReport(), Options{Quiet: true})
	want := "Scan errors (2):\n  worktrees /work/shop: not a git repository\n  walk aborted\n"
	if out != want {
		t.Errorf("got %q want %q", out, want)
	}
}
