package largeuntracked

import (
	"slices"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/testutil"
)

func TestParseEntries(t *testing.T) {
	out := "a b.bin\x00dir/sub/\x00new\nline\x00\x00../escape\x00/abs\x00./x.bin\x00"
	got := parseEntries(out, true)
	var rels []string
	for _, e := range got {
		rels = append(rels, e.rel)
	}
	want := []string{"a b.bin", "dir/sub", "new\nline", "x.bin"}
	if !slices.Equal(rels, want) {
		t.Fatalf("rels = %q, want %q", rels, want)
	}
	if got[0].dir || !got[1].dir || !got[0].ignored {
		t.Errorf("unexpected flags: %+v", got)
	}
	if len(parseEntries("", false)) != 0 {
		t.Error("empty output must yield no entries")
	}
}

func TestClaimsCovers(t *testing.T) {
	dir := testutil.ResolvedTempDir(t)
	for _, rel := range []string{"package.json", "generated/a.bin", "app/package.json", "loose/dist/x"} {
		testutil.WriteFile(t, dir, rel, "x")
	}
	cfg := config.Default()
	cfg.Detectors.BuildArtifacts.ExtraDirs = []string{"generated"}
	c, err := newClaims(dir, cfg)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		rel   string
		isDir bool
		want  bool
	}{
		{"node_modules", true, true},
		{"pkg/node_modules/x/y.bin", false, true},
		{"generated/a.bin", false, true},
		{"app/dist", true, true},
		{"app/dist/out.js", false, true},
		// Marker-gated: no project marker next to it, so not a build artifact.
		{"loose/dist", true, false},
		// A file that merely carries a build directory name is not a build dir.
		{"dist", false, false},
		{"src/main.go", false, false},
		{".agent/runs/2026/run.jsonl", false, true},
		{".claude/settings.json", false, false},
		{"notes/.DS_Store", false, true},
	}
	for _, tc := range tests {
		if got := c.Covers(tc.rel, tc.isDir); got != tc.want {
			t.Errorf("Covers(%q, %v) = %v, want %v", tc.rel, tc.isDir, got, tc.want)
		}
	}
}

func TestMinSize(t *testing.T) {
	cfg := config.Default()
	for in, want := range map[int64]int64{0: DefaultMinSizeBytes, -5: DefaultMinSizeBytes, 10: 10} {
		cfg.Detectors.LargeUntracked.MinSizeBytes = in
		if got := minSize(cfg); got != want {
			t.Errorf("minSize(%d) = %d, want %d", in, got, want)
		}
	}
}
