package catalog

import (
	"errors"
	"slices"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/config"
)

func TestLoadDecodesEmbeddedDataOnce(t *testing.T) {
	// Prime the once so the counter is stable whatever ran before.
	if _, err := Load(Options{}); err != nil {
		t.Fatal(err)
	}
	before := embeddedDecodes.Load()
	optionSets := []Options{
		{GOOS: "linux"},
		{GOOS: "windows"},
		{GOOS: "linux", Categories: map[string]bool{"ai": false}},
		{GOOS: "darwin", Tools: map[string]bool{"claude-code": false}},
	}
	for _, o := range optionSets {
		if _, err := Load(o); err != nil {
			t.Fatal(err)
		}
	}
	if got := embeddedDecodes.Load(); got != before {
		t.Errorf("embedded data decoded %d more times, want 0", got-before)
	}
}

func TestLoadMemoizesPerOptionSet(t *testing.T) {
	a, err := Load(Options{GOOS: "linux", Tools: map[string]bool{"x": false}})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := Load(Options{GOOS: "linux", Tools: map[string]bool{"x": false}})
	if a != b {
		t.Error("equal options should share one catalog")
	}
	c, _ := Load(Options{GOOS: "windows", Tools: map[string]bool{"x": false}})
	if a == c || c.GOOS() != "windows" {
		t.Error("different options must not share a catalog")
	}
}

func TestLoadSharedCatalogIsImmutable(t *testing.T) {
	a, _ := Load(Options{GOOS: "linux"})
	tools := a.Tools()
	tools[0].Name = "mutated"
	tools[0].Entries[0].Patterns[0] = "mutated"
	b, _ := Load(Options{GOOS: "linux"})
	if got := b.Tools()[0]; got.Name == "mutated" || got.Entries[0].Patterns[0] == "mutated" {
		t.Error("mutating a Tools() copy leaked into the shared catalog")
	}
}

func TestLoadFailuresAreNotMemoized(t *testing.T) {
	bad := Options{Extra: []config.CatalogTool{{ID: "Not Kebab", Name: "x"}}}
	for range 2 {
		if _, err := Load(bad); !errors.Is(err, ErrInvalid) {
			t.Fatalf("err = %v, want ErrInvalid", err)
		}
	}
	// A bad extra must not poison the embedded data for later loads.
	if _, err := Load(Options{}); err != nil {
		t.Fatal(err)
	}
}

func TestLoadExtrasDoNotLeakIntoEmbeddedTools(t *testing.T) {
	base, _ := Load(Options{GOOS: "linux"})
	id := base.Tools()[0].ID
	n := len(base.Tools()[0].Entries)
	_, err := Load(Options{GOOS: "linux", Extra: []config.CatalogTool{{ID: id, Description: "d", Project: []string{"leak-me"}}}})
	if err != nil {
		t.Fatal(err)
	}
	again, _ := Load(Options{GOOS: "linux", Tools: map[string]bool{"zzz": true}})
	tool, _ := again.Tool(id)
	if len(tool.Entries) != n {
		t.Errorf("entries = %d, want %d: extras leaked into the shared decode", len(tool.Entries), n)
	}
}

func TestBuildArtifactsDecodedOnceAndCopied(t *testing.T) {
	first, err := BuildArtifacts()
	if err != nil {
		t.Fatal(err)
	}
	before := buildArtifactsDecodes.Load()
	first[0].Dir = "mutated"
	first[0].Markers = append(first[0].Markers, "mutated")
	second, err := BuildArtifacts()
	if err != nil {
		t.Fatal(err)
	}
	if buildArtifactsDecodes.Load() != before {
		t.Error("BuildArtifacts decoded the embedded file again")
	}
	if second[0].Dir == "mutated" || slices.Contains(second[0].Markers, "mutated") {
		t.Error("caller mutation leaked into the cached entries")
	}
}

// BenchmarkLoad shows the per-target cost of a memoized Load; before the memo
// each call decoded and validated both embedded files (about 2 ms).
func BenchmarkLoad(b *testing.B) {
	for range b.N {
		if _, err := Load(Options{GOOS: "linux"}); err != nil {
			b.Fatal(err)
		}
	}
}
