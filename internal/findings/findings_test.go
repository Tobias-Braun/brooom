package findings

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// TestExampleReportRoundTrip keeps docs/findings.md honest: the example must
// decode without unknown fields, re-encode identically and carry totals that
// ComputeTotals agrees with.
func TestExampleReportRoundTrip(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "example-report.json"))
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var r Report
	if err := dec.Decode(&r); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if r.SchemaVersion != SchemaVersion {
		t.Fatalf("schema_version = %d, want %d", r.SchemaVersion, SchemaVersion)
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	if err := enc.Encode(&r); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(bytes.TrimSpace(buf.Bytes()), bytes.TrimSpace(raw)) {
		t.Errorf("re-encoded report differs from testdata/example-report.json:\n%s", buf.String())
	}
	if got := ComputeTotals(r.Findings); !reflect.DeepEqual(got, r.Totals) {
		t.Errorf("totals in example = %+v, computed = %+v", r.Totals, got)
	}
}

func TestNewIDDeterministic(t *testing.T) {
	a := NewID("build-artifacts", KindDir, "/p/node_modules", "")
	b := NewID("build-artifacts", KindDir, "/p/./node_modules", "")
	if a != b {
		t.Errorf("IDs differ for equivalent paths: %s vs %s", a, b)
	}
	if len(a) != 16 {
		t.Errorf("ID length = %d, want 16", len(a))
	}
	if NewID("stale-branch", KindBranch, "/p", "a") == NewID("stale-branch", KindBranch, "/p", "b") {
		t.Error("different refs must yield different IDs")
	}
	// The separator between parts prevents ambiguous concatenations.
	if NewID("a", KindFile, "b", "") == NewID("ab", KindFile, "", "") {
		t.Error("ID parts are not separated")
	}
}

func TestTopLevel(t *testing.T) {
	sep := string(filepath.Separator)
	p := func(parts ...string) string { return sep + filepath.Join(parts...) }
	in := []string{p("a", "b", "c"), p("a", "b-x"), p("a", "b"), p("a", "b"), p("z")}
	want := []string{p("a", "b"), p("a", "b-x"), p("z")}
	if got := TopLevel(in); !reflect.DeepEqual(got, want) {
		t.Errorf("TopLevel = %v, want %v", got, want)
	}
}

func TestReclaimableDedupesNestedAndSkipsBlocked(t *testing.T) {
	sep := string(filepath.Separator)
	trash := SuggestedAction{Type: ActionTrash}
	fs := []Finding{
		{Path: sep + "p", Kind: KindDir, SizeBytes: 100, SuggestedAction: trash},
		{Path: sep + filepath.Join("p", "node_modules"), Kind: KindDir, SizeBytes: 60, SuggestedAction: trash},
		{Path: sep + "q", Kind: KindFile, SizeBytes: 5, SuggestedAction: SuggestedAction{Type: ActionNone}},
		{Path: sep + "r", Kind: KindGitObjects, SizeBytes: 7, SuggestedAction: SuggestedAction{Type: ActionGitGC}},
	}
	if got := Reclaimable(fs); got != 107 {
		t.Errorf("Reclaimable = %d, want 107", got)
	}
}

func TestBlockingFlags(t *testing.T) {
	f := Finding{RiskFlags: []RiskFlag{RiskGitignored}}
	if f.Blocked() {
		t.Error("gitignored must not block")
	}
	f.RiskFlags = append(f.RiskFlags, RiskWorktreeDirty)
	if !f.Blocked() {
		t.Error("worktree_dirty must block")
	}
	for _, r := range AllRiskFlags() {
		if r == "" {
			t.Error("empty risk flag")
		}
	}
}

// TestRetiredRiskFlag guards the removal of uncommitted_changes: it was
// documented as blocking but no detector ever set it, so it must not be
// advertised as a known flag again unless a detector emits it.
func TestRetiredRiskFlag(t *testing.T) {
	for _, r := range AllRiskFlags() {
		if r == "uncommitted_changes" {
			t.Errorf("%s is listed although no detector emits it", r)
		}
	}
	if RiskFlag("uncommitted_changes").Blocking() {
		t.Error("a retired flag must not pretend to block")
	}
}

func TestNewReportSortsAndNeverNil(t *testing.T) {
	r := NewReport("dev", time.Unix(0, 0), nil, nil, nil)
	if r.Findings == nil {
		t.Fatal("Findings must be an empty slice, not nil, so JSON renders []")
	}
	r = NewReport("dev", time.Unix(0, 0), nil, []Finding{{Detector: "b"}, {Detector: "a"}}, nil)
	if r.Findings[0].Detector != "a" {
		t.Errorf("findings not sorted: %v", r.Findings)
	}
}

func TestActionableWithForce(t *testing.T) {
	dirty := []RiskFlag{RiskWorktreeDirty, RiskGitignored}
	if Actionable(dirty, false) {
		t.Error("dirty worktree must block without --force")
	}
	if !Actionable(dirty, true) {
		t.Error("--force must override worktree_dirty")
	}
	if Actionable([]RiskFlag{RiskProtectedBranch}, true) {
		t.Error("--force must never override protected_branch")
	}
	if !Actionable(nil, false) {
		t.Error("no flags must be actionable")
	}
}
