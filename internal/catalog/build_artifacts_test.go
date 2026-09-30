package catalog

import (
	"errors"
	"io/fs"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
)

// baEntry is a minimal valid build artifact entry.
const baEntry = `{"id":"x","ecosystem":"node","dir":"node_modules","markers":["package.json"],"description":"d"}`

func baFile(entries ...string) string {
	return `{"schema_version":1,"entries":[` + strings.Join(entries, ",") + `]}`
}

func decodeBA(t *testing.T, content string) ([]BuildArtifactEntry, error) {
	t.Helper()
	return buildArtifactsFrom(fstest.MapFS{"b.json": {Data: []byte(content)}}, "b.json")
}

// TestEmbeddedBuildArtifacts is the CI gate for the embedded file: it must
// pass its own strict decoder and cover the documented ecosystems.
func TestEmbeddedBuildArtifacts(t *testing.T) {
	entries, err := BuildArtifacts()
	if err != nil {
		t.Fatal(err)
	}
	dirs := map[string]bool{}
	for _, e := range entries {
		checkComplete(t, e)
		dirs[e.Dir] = true
	}
	for _, d := range []string{
		"node_modules", ".next", ".nuxt", ".output", ".svelte-kit", ".turbo", ".parcel-cache", ".angular/cache", ".expo",
		"dist", "build", "out", "target", ".gradle", ".venv", "venv", "env", "__pycache__", "*.egg-info", ".tox",
		"bin", "obj", "Pods", ".dart_tool", "_build", "deps", "zig-cache", ".zig-cache", ".terraform", "vendor",
	} {
		if !dirs[d] {
			t.Errorf("catalog lacks %q", d)
		}
	}
}

func checkComplete(t *testing.T, e BuildArtifactEntry) {
	t.Helper()
	if e.ID == "" || e.Dir == "" || e.Description == "" || e.Ecosystem == "" {
		t.Errorf("incomplete entry %+v", e)
	}
	if !slices.Contains(knownConfidences, e.ConfidenceCap) {
		t.Errorf("%s: confidence_cap %q", e.ID, e.ConfidenceCap)
	}
}

// TestBuildArtifactSafetyProperties pins the decisions that protect users:
// generic names need markers, venvs need pyvenv.cfg, vendor is low.
func TestBuildArtifactSafetyProperties(t *testing.T) {
	entries, err := BuildArtifacts()
	if err != nil {
		t.Fatal(err)
	}
	rules := []struct {
		dirs []string
		ok   func(BuildArtifactEntry) bool
		msg  string
	}{
		{[]string{"dist", "build", "out", "target", "bin", "obj", "deps"}, func(e BuildArtifactEntry) bool { return len(e.Markers) > 0 }, "must be marker gated"},
		{[]string{".venv", "venv", "env"}, func(e BuildArtifactEntry) bool { return e.RequireFileInside == "pyvenv.cfg" }, "must require pyvenv.cfg"},
		{[]string{"vendor"}, func(e BuildArtifactEntry) bool {
			return e.ConfidenceCap == ConfidenceLow && e.MarkerMode == MarkerModeAll
		}, "must be low and need all markers"},
		{[]string{".terraform"}, func(e BuildArtifactEntry) bool { return e.ConfidenceCap == ConfidenceMedium }, "must be capped at medium"},
	}
	for _, e := range entries {
		for _, r := range rules {
			if slices.Contains(r.dirs, e.Dir) && !r.ok(e) {
				t.Errorf("%s %s", e.ID, r.msg)
			}
		}
	}
}

func TestBuildArtifactIDsUnique(t *testing.T) {
	entries, err := BuildArtifacts()
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, e := range entries {
		if seen[e.ID] {
			t.Errorf("duplicate id %s", e.ID)
		}
		seen[e.ID] = true
	}
}

func TestBuildArtifactDefaults(t *testing.T) {
	got, err := decodeBA(t, baFile(baEntry))
	if err != nil {
		t.Fatal(err)
	}
	if got[0].MarkerMode != MarkerModeAny || got[0].ConfidenceCap != ConfidenceHigh {
		t.Fatalf("defaults not applied: %+v", got[0])
	}
}

func TestBuildArtifactsRejects(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{"unknown field", baFile(`{"id":"x","ecosystem":"node","dir":"a","description":"d","extra":1}`), "unknown field"},
		{"tools format", `{"schema_version":1,"tools":[]}`, "unknown field"},
		{"trailing data", baFile(baEntry) + ` {}`, "unexpected data"},
		{"wrong version", `{"schema_version":2,"entries":[]}`, "schema_version"},
		{"no entries", baFile(), "at least one entry"},
		{"empty id", baFile(`{"id":"","ecosystem":"node","dir":"a","description":"d"}`), "kebab-case"},
		{"bad id", baFile(`{"id":"Bad_ID","ecosystem":"node","dir":"a","description":"d"}`), "kebab-case"},
		{"empty ecosystem", baFile(`{"id":"x","ecosystem":"","dir":"a","description":"d"}`), "ecosystem"},
		{"empty dir", baFile(`{"id":"x","ecosystem":"node","dir":"","description":"d"}`), "dir must not be empty"},
		{"three segments", baFile(`{"id":"x","ecosystem":"node","dir":"a/b/c","description":"d"}`), "two segments"},
		{"dotdot dir", baFile(`{"id":"x","ecosystem":"node","dir":"../a","description":"d"}`), "relative"},
		{"bad glob dir", baFile(`{"id":"x","ecosystem":"node","dir":"[a","description":"d"}`), "dir"},
		{"marker with separator", baFile(`{"id":"x","ecosystem":"node","dir":"a","markers":["b/c"],"description":"d"}`), "marker"},
		{"bad marker glob", baFile(`{"id":"x","ecosystem":"node","dir":"a","markers":["[b"],"description":"d"}`), "marker"},
		{"bad marker mode", baFile(`{"id":"x","ecosystem":"node","dir":"a","marker_mode":"some","description":"d"}`), "marker_mode"},
		{"bad cap", baFile(`{"id":"x","ecosystem":"node","dir":"a","confidence_cap":"certain","description":"d"}`), "confidence_cap"},
		{"require file glob", baFile(`{"id":"x","ecosystem":"node","dir":"a","require_file_inside":"*.cfg","description":"d"}`), "require_file_inside"},
		{"empty description", baFile(`{"id":"x","ecosystem":"node","dir":"a","description":" "}`), "description"},
		{"duplicate id", baFile(baEntry, baEntry), "duplicate"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := decodeBA(t, tt.content)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tt.want)
			}
		})
	}
}

func TestBuildArtifactsValidationIsErrInvalid(t *testing.T) {
	_, err := decodeBA(t, baFile(`{"id":"","ecosystem":"","dir":"","description":""}`))
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid", err)
	}
}

func TestBuildArtifactsMissingFile(t *testing.T) {
	if _, err := buildArtifactsFrom(fstest.MapFS{}, "b.json"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("err = %v", err)
	}
}

// TestDecodersAreSeparate proves both directions: the tools loader ignores
// the embedded build artifact file, and the build artifact decoder refuses a
// file in the tools format.
func TestDecodersAreSeparate(t *testing.T) {
	if _, err := Load(Options{}); err != nil {
		t.Fatalf("tools loader: %v", err)
	}
	for _, name := range toolFiles {
		if name == buildArtifactsFile {
			t.Fatalf("%s must not be a tool file", name)
		}
		sub, err := fs.Sub(dataFS, "data")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := buildArtifactsFrom(sub, name); err == nil {
			t.Errorf("build artifact decoder accepted the tools file %s", name)
		}
	}
	// The tools loader must not be affected by a broken build artifact file.
	fsys := fstest.MapFS{
		"ai_tools.json":        {Data: []byte(fileJSON(toolJSON("a", "")))},
		"build_artifacts.json": {Data: []byte(`not json`)},
	}
	if _, err := loadFrom(fsys, toolFiles[:1], Options{GOOS: "linux"}); err != nil {
		t.Fatal(err)
	}
}
