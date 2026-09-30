package buildartifacts

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/testutil"
)

// claimTree is a synthetic tree with claimed and unclaimed directories.
var claimTree = []string{
	"package.json", "node_modules/x/index.js", "node_modules/x/node_modules/y/z.js",
	".venv/pyvenv.cfg", ".venv/lib/__pycache__/m.pyc",
	"lib/dist/out.js", "src/main.go", "pkg/app.py", "pkg/__pycache__/app.pyc",
	"plain-env/notes.txt", "sub/node_modules/x",
}

func TestClaimsTable(t *testing.T) {
	dir := testutil.ResolvedTempDir(t)
	for _, rel := range claimTree {
		testutil.WriteFile(t, dir, rel, "x")
	}
	claims, err := Claims(dir)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		rel   string
		isDir bool
		want  bool
	}{
		{"node_modules", true, true},
		{"node_modules", false, false},
		{".venv", true, true},
		{"lib/dist", true, false},
		{"src", true, false},
		{"plain-env", true, false},
		{"pkg/__pycache__", true, true},
		{"sub/node_modules", true, true},
		{"node_modules/x/node_modules", true, true},
		{"missing/node_modules", true, true},
		{"", true, false},
		{".", true, false},
		{"/node_modules/", true, true},
		{filepath.Join("pkg", "__pycache__"), true, true},
	}
	for _, tt := range tests {
		if got := claims(tt.rel, tt.isDir); got != tt.want {
			t.Errorf("Claims(%q, %v) = %v, want %v", tt.rel, tt.isDir, got, tt.want)
		}
	}
}

// TestClaimsAgreesWithDetector walks a synthetic tree with the claim
// matcher, the way the large-untracked detector will, and requires exactly
// the directories the detector reports.
func TestClaimsAgreesWithDetector(t *testing.T) {
	f := newFixture(t, false)
	f.write(claimTree...)
	f.settle(f.daysAgo(100))
	detected := rels(f.byRel())
	claims, err := Claims(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	var claimed []string
	err = filepath.WalkDir(f.dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == f.dir || !d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(f.dir, p)
		if claims(rel, true) {
			claimed = append(claimed, filepath.ToSlash(rel))
			return filepath.SkipDir
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(claimed)
	if !slices.Equal(claimed, detected) {
		t.Fatalf("Claims %v, detector %v", claimed, detected)
	}
	want := []string{".venv", "node_modules", "pkg/__pycache__", "sub/node_modules"}
	if !slices.Equal(claimed, want) {
		t.Fatalf("claimed %v, want %v", claimed, want)
	}
}

func TestClaimsWithConfig(t *testing.T) {
	dir := testutil.ResolvedTempDir(t)
	testutil.WriteFile(t, dir, "gen/x", "x")
	testutil.WriteFile(t, dir, "dist/x", "x")
	claims, err := ClaimsWith(dir, config.BuildArtifacts{ExtraDirs: []string{"gen"}})
	if err != nil {
		t.Fatal(err)
	}
	if !claims("gen", true) || !claims("deep/gen", true) || claims("dist", true) {
		t.Fatal("marker-less extras claim everywhere; marker-gated catalog names stay gated")
	}
	claims, err = ClaimsWith(dir, config.BuildArtifacts{Dirs: []string{"gen"}})
	if err != nil {
		t.Fatal(err)
	}
	if !claims("gen", true) || claims("node_modules", true) {
		t.Fatal("dirs replace the catalog")
	}
}

func TestClaimsErrors(t *testing.T) {
	dir := testutil.ResolvedTempDir(t)
	file := testutil.WriteFile(t, dir, "f.txt", "x")
	if _, err := Claims(filepath.Join(dir, "missing")); err == nil {
		t.Error("missing dir")
	}
	if _, err := Claims(file); err == nil {
		t.Error("file instead of directory")
	}
	if _, err := ClaimsWith(dir, config.BuildArtifacts{ExtraDirs: []string{""}}); err == nil {
		t.Error("invalid config")
	}
}

// TestClaimsIsReadOnly guards the purity promise: matching leaves the tree
// exactly as it was.
func TestClaimsIsReadOnly(t *testing.T) {
	dir := testutil.ResolvedTempDir(t)
	for _, rel := range claimTree {
		testutil.WriteFile(t, dir, rel, "x")
	}
	before := snapshot(t, dir)
	claims, err := Claims(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range claimTree {
		claims(rel, true)
	}
	if after := snapshot(t, dir); !slices.Equal(before, after) {
		t.Fatalf("tree changed: %v -> %v", before, after)
	}
}

func snapshot(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil {
			fi, _ := d.Info()
			out = append(out, p+"|"+fi.ModTime().String())
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestMarkerNamesFoldOnCaseInsensitiveSystems covers Package.json on macOS
// and Windows by switching the folding on for a case-sensitive Linux tree.
func TestMarkerNamesFoldOnCaseInsensitiveSystems(t *testing.T) {
	tests := []struct {
		name string
		fold bool
		want bool
	}{
		{"case-insensitive", true, true},
		{"case-sensitive", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			old := foldCase
			foldCase = tt.fold
			t.Cleanup(func() { foldCase = old })
			dir := testutil.ResolvedTempDir(t)
			testutil.WriteFile(t, dir, "Package.json", "{}")
			testutil.WriteFile(t, dir, "Dist/x.js", "x")
			testutil.WriteFile(t, dir, "sub/Package.JSON", "{}")
			testutil.WriteFile(t, dir, "sub/Node_Modules/x", "x")
			claims, err := Claims(dir)
			if err != nil {
				t.Fatal(err)
			}
			if claims("Dist", true) != tt.want {
				t.Errorf("Dist claimed = %v, want %v", !tt.want, tt.want)
			}
			if claims("sub/Node_Modules", true) != tt.want {
				t.Errorf("Node_Modules claimed = %v, want %v", !tt.want, tt.want)
			}
		})
	}
}

func TestMatcherListsEachParentOnce(t *testing.T) {
	dir := testutil.ResolvedTempDir(t)
	testutil.WriteFile(t, dir, "package.json", "{}")
	m, err := newMatcher(dir, config.Default().Detectors.BuildArtifacts)
	if err != nil {
		t.Fatal(err)
	}
	m.match("node_modules", true)
	// Removing the marker after the first lookup proves the listing is
	// cached rather than stat'ed per rule.
	if err := os.Remove(filepath.Join(dir, "package.json")); err != nil {
		t.Fatal(err)
	}
	if got, ok := m.match("dist", true); !ok || got.marker != "package.json" {
		t.Fatalf("match = %+v %v", got, ok)
	}
	if len(m.listings) != 1 {
		t.Fatalf("listings = %d", len(m.listings))
	}
}
