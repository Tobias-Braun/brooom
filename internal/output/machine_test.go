package output

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/findings"
)

func TestNamesListAllFormats(t *testing.T) {
	got := strings.Join(Names(), ",")
	if want := "json,ndjson,plain,summary,table,tree"; got != want {
		t.Errorf("Names() = %s, want %s", got, want)
	}
}

func TestJSONGolden(t *testing.T) {
	assertGolden(t, "json", render(t, "json", extendedReport(), Options{}))
}

func TestJSONIgnoresOptionsAndHasNoANSI(t *testing.T) {
	plain := render(t, "json", extendedReport(), Options{})
	styled := render(t, "json", extendedReport(), Options{Color: true, Quiet: true, Width: 20})
	if plain != styled {
		t.Error("json output depends on Options")
	}
	if strings.Contains(plain, "\x1b") {
		t.Error("json contains ANSI escapes")
	}
	if !strings.HasSuffix(plain, "}\n") || strings.HasSuffix(plain, "\n\n") {
		t.Error("json must end with exactly one newline")
	}
	for _, escaped := range []string{"\\u0026", "\\u003c", "\\u003e"} {
		if strings.Contains(plain, escaped) {
			t.Errorf("json contains HTML escape %s", escaped)
		}
	}
	if !strings.Contains(plain, "a&b<c>.log") {
		t.Error("path with &<> not rendered literally")
	}
}

// nilReport has nil for everything the schema wants as array or object.
func nilReport() *findings.Report {
	return &findings.Report{
		SchemaVersion: 1, GeneratedAt: fixtureNow,
		Findings: []findings.Finding{{ID: "x", Detector: "d", Path: "/p", Kind: findings.KindFile,
			Scope: findings.Scope{Type: findings.ScopeRepo, Path: "/p"}, Confidence: findings.ConfidenceHigh,
			SuggestedAction: findings.SuggestedAction{Type: findings.ActionTrash}}},
	}
}

func TestJSONNeverNull(t *testing.T) {
	out := render(t, "json", nilReport(), Options{})
	if strings.Contains(out, "null") {
		t.Errorf("json contains null:\n%s", out)
	}
	for _, want := range []string{`"scopes": []`, `"evidence": []`, `"risk_flags": []`, `"by_detector": {}`} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s in\n%s", want, out)
		}
	}
	empty := render(t, "json", &findings.Report{}, Options{})
	if strings.Contains(empty, "null") || !strings.Contains(empty, `"findings": []`) {
		t.Errorf("empty report renders null or omits findings:\n%s", empty)
	}
}

func TestJSONDoesNotMutateInput(t *testing.T) {
	r := nilReport()
	before, _ := json.Marshal(r)
	render(t, "json", r, Options{})
	render(t, "ndjson", r, Options{})
	after, _ := json.Marshal(r)
	if !bytes.Equal(before, after) {
		t.Error("formatting mutated the report")
	}
	if r.Scopes != nil || r.Findings[0].Evidence != nil || r.Totals.ByDetector != nil {
		t.Error("nil slices/maps of the input were replaced")
	}
}

func TestJSONRoundTrip(t *testing.T) {
	in := extendedReport()
	out := render(t, "json", in, Options{})
	dec := json.NewDecoder(strings.NewReader(out))
	dec.DisallowUnknownFields()
	var got findings.Report
	if err := dec.Decode(&got); err != nil {
		t.Fatal(err)
	}
	// Decoding turns nil evidence into empty slices, exactly what the
	// formatter normalizes, so compare against the normalized input.
	want := normalizeReport(in)
	if !reflect.DeepEqual(&got, want) {
		gj, _ := json.Marshal(&got)
		wj, _ := json.Marshal(want)
		t.Errorf("round trip differs\n got: %s\nwant: %s", gj, wj)
	}
}

func TestNDJSONGolden(t *testing.T) {
	assertGolden(t, "ndjson", render(t, "ndjson", extendedReport(), Options{}))
}

func TestNDJSONShape(t *testing.T) {
	r := extendedReport()
	out := render(t, "ndjson", r, Options{Color: true, Quiet: true})
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) != len(r.Findings) {
		t.Fatalf("%d lines for %d findings", len(lines), len(r.Findings))
	}
	for i, l := range lines {
		checkNDJSONLine(t, l, r.Findings[i])
	}
	if strings.Contains(out, "git count-objects") {
		t.Error("report errors leaked into the stream")
	}
}

// checkNDJSONLine asserts one line is a compact, normalized finding equal in
// identity to want.
func checkNDJSONLine(t *testing.T, line string, want findings.Finding) {
	t.Helper()
	var f findings.Finding
	if err := json.Unmarshal([]byte(line), &f); err != nil {
		t.Fatalf("%v: %s", err, line)
	}
	if strings.ContainsAny(line, "\x1b") || strings.Contains(line, "null") || strings.Contains(line, `": `) {
		t.Errorf("line not compact/normalized: %s", line)
	}
	if f.ID != want.ID || f.Path != want.Path {
		t.Errorf("line out of order: %s", line)
	}
}

func TestNDJSONEmptyWritesNothing(t *testing.T) {
	if out := render(t, "ndjson", emptyReport(), Options{}); out != "" {
		t.Errorf("empty report wrote %q", out)
	}
	if out := render(t, "ndjson", errorsOnlyReport(), Options{}); out != "" {
		t.Errorf("errors-only report wrote %q", out)
	}
}

// streamSource is how a scan command consumes the formatter: through a local
// interface with plain func types, so no named type is shared.
type streamSource interface {
	NewStream(w io.Writer, opts Options) (onFinding func(findings.Finding), finish func() error)
}

func TestNDJSONStreamMatchesFormatter(t *testing.T) {
	r := extendedReport()
	f, _ := Get("ndjson")
	src, ok := f.(streamSource)
	if !ok {
		t.Fatal("ndjson formatter does not implement the NewStream contract")
	}
	var buf bytes.Buffer
	on, finish := src.NewStream(&buf, Options{})
	for _, fi := range r.Findings {
		on(fi)
	}
	if err := finish(); err != nil {
		t.Fatal(err)
	}
	assertGolden(t, "ndjson", buf.String())
	if buf.String() != render(t, "ndjson", r, Options{}) {
		t.Error("stream bytes differ from formatter bytes")
	}
}

// countingWriter records every Write call to prove nothing is buffered.
type countingWriter struct {
	mu     sync.Mutex
	writes [][]byte
	failAt int // fail from the n-th write on (1-based), 0 = never
}

var errBroken = errors.New("broken pipe")

func (c *countingWriter) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.writes = append(c.writes, append([]byte(nil), p...))
	if c.failAt > 0 && len(c.writes) >= c.failAt {
		return 0, errBroken
	}
	return len(p), nil
}

func TestNDJSONStreamUnbufferedAndSticky(t *testing.T) {
	cw := &countingWriter{failAt: 2}
	s := NewNDJSONStream(cw)
	r := extendedReport()
	s.OnFinding(r.Findings[0])
	if len(cw.writes) != 1 || s.Err() != nil {
		t.Fatalf("first finding must be written immediately (writes=%d err=%v)", len(cw.writes), s.Err())
	}
	s.OnFinding(r.Findings[1])
	if !errors.Is(s.Err(), errBroken) {
		t.Fatalf("Err = %v, want broken pipe", s.Err())
	}
	s.OnFinding(r.Findings[2])
	if len(cw.writes) != 2 {
		t.Errorf("findings after the first error must be ignored, got %d writes", len(cw.writes))
	}
}

func TestNDJSONStreamConcurrent(t *testing.T) {
	var buf bytes.Buffer
	s := NewNDJSONStream(&buf)
	r := extendedReport()
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for _, f := range r.Findings {
				s.OnFinding(f)
			}
		}()
	}
	wg.Wait()
	lines := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
	if len(lines) != 8*len(r.Findings) {
		t.Fatalf("got %d lines", len(lines))
	}
	for _, l := range lines {
		if !json.Valid([]byte(l)) {
			t.Fatalf("interleaved line: %q", l)
		}
	}
}

func TestPlainGolden(t *testing.T) {
	assertGolden(t, "plain", render(t, "plain", extendedReport(), Options{}))
}

func TestPlainOmitsUnsafeFindings(t *testing.T) {
	out := render(t, "plain", extendedReport(), Options{Color: true, Quiet: true, Width: 10})
	if strings.Contains(out, "\x1b") {
		t.Error("plain contains ANSI")
	}
	for _, banned := range []string{"feat/wip", "shop-wt\n", "gone-wt"} {
		if strings.Contains(out, banned) {
			t.Errorf("plain lists %q", banned)
		}
	}
	if !strings.Contains(out, "/work/shop\tfeat/old-cart\n") {
		t.Errorf("branch row missing or malformed:\n%s", out)
	}
	// The repository root alone (git-gc finding) must never be a line.
	for _, l := range strings.Split(out, "\n") {
		if l == "/work/shop" {
			t.Error("repository root listed on its own")
		}
	}
}

func TestPlainLineBreakError(t *testing.T) {
	f, _ := Get("plain")
	var buf bytes.Buffer
	err := f.Write(&buf, lineBreakReport(), Options{})
	if err == nil || err.Error() != "plain: 2 findings skipped because their path contains a line break (use --format json)" {
		t.Fatalf("err = %v", err)
	}
	assertGolden(t, "plain_linebreak", buf.String())
}

func TestPlainListable(t *testing.T) {
	act := findings.SuggestedAction{Type: findings.ActionTrash}
	tests := []struct {
		name string
		f    findings.Finding
		want bool
	}{
		{"file", findings.Finding{Kind: findings.KindFile, SuggestedAction: act}, true},
		{"dir", findings.Finding{Kind: findings.KindDir, SuggestedAction: act}, true},
		{"worktree", findings.Finding{Kind: findings.KindWorktree, SuggestedAction: act}, true},
		{"branch", findings.Finding{Kind: findings.KindBranch, SuggestedAction: act}, true},
		{"flagged", findings.Finding{Kind: findings.KindDir, SuggestedAction: findings.SuggestedAction{Type: findings.ActionNone}}, false},
		{"no action", findings.Finding{Kind: findings.KindDir}, false},
		{"worktree-missing", findings.Finding{Kind: findings.KindWorktreeMissing, SuggestedAction: act}, false},
		{"loose objects", findings.Finding{Kind: findings.KindGitObjects, SuggestedAction: act}, false},
		{"packs", findings.Finding{Kind: findings.KindGitPacks, SuggestedAction: act}, false},
		{"reflog", findings.Finding{Kind: findings.KindGitReflog, SuggestedAction: act}, false},
		{"large blob", findings.Finding{Kind: findings.KindGitLargeBlob, SuggestedAction: act}, false},
		{"unknown kind", findings.Finding{Kind: "future", SuggestedAction: act}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := plainListable(tt.f); got != tt.want {
				t.Errorf("plainListable = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPlainEmptyWritesNothing(t *testing.T) {
	if out := render(t, "plain", emptyReport(), Options{}); out != "" {
		t.Errorf("wrote %q", out)
	}
}
