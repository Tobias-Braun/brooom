package config

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// overlayFixture builds a directory tree with two nested configured roots
// (ws and ws/nested), a sibling ws2 and a lookalike wsx, and returns the
// (symlink-resolved) base plus a config with those roots.
func overlayFixture(t *testing.T) (string, *Config) {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"ws/nested/repo", "ws/repo", "ws2/repo", "wsx/repo", "other/repo"} {
		if err := os.MkdirAll(filepath.Join(base, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cfg := Default()
	cfg.Roots = []Root{
		{
			Path:       filepath.Join(base, "ws"),
			Exclude:    []string{"node_modules"},
			Thresholds: &ThresholdOverrides{MinAgeDays: intp(30)},
			Detectors:  map[string]bool{"build-artifacts": false},
		},
		{
			Path:       filepath.Join(base, "ws", "nested"),
			Exclude:    []string{"vendor"},
			Thresholds: &ThresholdOverrides{MinAgeDays: intp(60), RecentDays: intp(5)},
		},
	}
	return base, cfg
}

func TestForTargetRootSelection(t *testing.T) {
	base, cfg := overlayFixture(t)
	ws := filepath.Join(base, "ws")
	nested := filepath.Join(ws, "nested")
	tests := []struct {
		name, hint, target string
		wantRoot           string
		wantExclude        []string
		wantMinAge         int
		buildArtifacts     bool
	}{
		{"hint equals configured root", ws, filepath.Join(ws, "repo"), ws, []string{"node_modules"}, 30, false},
		{"hint equals nested root", nested, filepath.Join(nested, "repo"), nested, []string{"vendor"}, 60, true},
		{"empty hint picks longest containing root", "", filepath.Join(nested, "repo"), nested, []string{"vendor"}, 60, true},
		{"repo mode hint inside root but not configured", filepath.Join(ws, "repo"), filepath.Join(ws, "repo"), ws, []string{"node_modules"}, 30, false},
		{"repo mode equals empty hint result", "", filepath.Join(ws, "repo"), ws, []string{"node_modules"}, 30, false},
		{"user location hint outside all roots skips root layer", filepath.Join(base, "other"), filepath.Join(base, "other", "repo"), "", nil, 14, true},
		{"non matching hint is not an error", filepath.Join(base, "other"), filepath.Join(ws, "repo"), ws, []string{"node_modules"}, 30, false},
		{"sibling with shared prefix is not contained", "", filepath.Join(base, "wsx", "repo"), "", nil, 14, true},
		{"ws2 not contained in ws", "", filepath.Join(base, "ws2", "repo"), "", nil, 14, true},
		{"target equals root", "", ws, ws, []string{"node_modules"}, 30, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eff, err := cfg.ForTarget(tt.hint, tt.target)
			if err != nil {
				t.Fatal(err)
			}
			if eff.RootPath != tt.wantRoot {
				t.Errorf("RootPath = %q, want %q", eff.RootPath, tt.wantRoot)
			}
			if !slices.Equal(eff.RootExclude, tt.wantExclude) {
				t.Errorf("RootExclude = %v, want %v", eff.RootExclude, tt.wantExclude)
			}
			if eff.Thresholds.MinAgeDays != tt.wantMinAge {
				t.Errorf("MinAgeDays = %d, want %d", eff.Thresholds.MinAgeDays, tt.wantMinAge)
			}
			if eff.Detectors.BuildArtifacts.Enabled != tt.buildArtifacts {
				t.Errorf("build-artifacts enabled = %v", eff.Detectors.BuildArtifacts.Enabled)
			}
		})
	}
}

func TestForTargetRepoModeMatchesWorkspaceMode(t *testing.T) {
	base, cfg := overlayFixture(t)
	target := filepath.Join(base, "ws", "repo")
	a, err := cfg.ForTarget(target, target)
	if err != nil {
		t.Fatal(err)
	}
	b, err := cfg.ForTarget("", target)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a, b) {
		t.Error("repo mode and workspace mode must produce the same overlay")
	}
}

func TestForTargetRootOverridesNeedNotTighten(t *testing.T) {
	base, cfg := overlayFixture(t)
	cfg.Roots[0].Thresholds = &ThresholdOverrides{MinAgeDays: intp(0), MinSizeBytes: int64p(7), RecentDays: intp(0)}
	eff, err := cfg.ForTarget("", filepath.Join(base, "ws", "repo"))
	if err != nil {
		t.Fatal(err)
	}
	if eff.Thresholds != (Thresholds{MinAgeDays: 0, MinSizeBytes: 7, RecentDays: 0}) {
		t.Errorf("root overrides not applied: %+v", eff.Thresholds)
	}
}

func TestForTargetDoesNotMutateReceiver(t *testing.T) {
	base, cfg := overlayFixture(t)
	target := filepath.Join(base, "ws", "repo")
	writeRepoConfig(t, target, `{"disable":["worktrees"],"thresholds":{"min_age_days":100},"protected_branches":["keep"],"exclude":["gen"]}`)
	before := cfg.clone()
	eff, err := cfg.ForTarget("", target)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg, before) {
		t.Fatal("receiver was mutated")
	}
	// Mutating the result must not leak back either (no aliasing).
	eff.Git.ProtectedBranches[0] = "changed"
	eff.Roots[0].Exclude[0] = "changed"
	*eff.Roots[0].Thresholds.MinAgeDays = 999
	eff.Roots[0].Detectors["build-artifacts"] = true
	if !reflect.DeepEqual(cfg, before) {
		t.Fatal("effective config aliases the receiver")
	}
}

func TestForTargetAppliesRepoConfig(t *testing.T) {
	base, cfg := overlayFixture(t)
	target := filepath.Join(base, "ws", "repo")
	writeRepoConfig(t, target, `{"disable":["worktrees"],"thresholds":{"min_age_days":100},"protected_branches":["keep"],"exclude":["gen"]}`)
	eff, err := cfg.ForTarget("", target)
	if err != nil {
		t.Fatal(err)
	}
	if eff.Detectors.Worktrees.Enabled || eff.Thresholds.MinAgeDays != 100 {
		t.Errorf("repo config not applied: %+v", eff.Thresholds)
	}
	if !slices.Equal(eff.RepoExclude, []string{"gen"}) || !slices.Equal(eff.RootExclude, []string{"node_modules"}) {
		t.Errorf("excludes: repo %v root %v", eff.RepoExclude, eff.RootExclude)
	}
	if !slices.Contains(eff.Git.ProtectedBranches, "keep") {
		t.Error("protected branch not added")
	}
}

func TestForTargetRepoConfigCannotLowerRootThreshold(t *testing.T) {
	base, cfg := overlayFixture(t)
	target := filepath.Join(base, "ws", "repo")
	writeRepoConfig(t, target, `{"thresholds":{"min_age_days":7}}`)
	_, err := cfg.ForTarget("", target)
	want := ".brooom.json: thresholds.min_age_days: 7 is lower than the effective value 30; repo config may only tighten"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("want %q, got %v", want, err)
	}
}

func TestForTargetSymlinks(t *testing.T) {
	base, cfg := overlayFixture(t)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(filepath.Join(base, "ws"), link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	// Target reached through a symlink into a configured root.
	eff, err := cfg.ForTarget("", filepath.Join(link, "repo"))
	if err != nil {
		t.Fatal(err)
	}
	if eff.RootPath != filepath.Join(base, "ws") {
		t.Errorf("symlinked target: RootPath = %q", eff.RootPath)
	}
	// Root configured through a symlink, target given by its real path.
	cfg.Roots = []Root{{Path: link, Exclude: []string{"x"}}}
	eff, err = cfg.ForTarget("", filepath.Join(base, "ws", "repo"))
	if err != nil {
		t.Fatal(err)
	}
	if eff.RootPath != filepath.Join(base, "ws") || !slices.Equal(eff.RootExclude, []string{"x"}) {
		t.Errorf("symlinked root: %q %v", eff.RootPath, eff.RootExclude)
	}
	// The hint may also be spelled through the link.
	eff, err = cfg.ForTarget(link, filepath.Join(base, "other", "repo"))
	if err != nil {
		t.Fatal(err)
	}
	if eff.RootPath != filepath.Join(base, "ws") {
		t.Errorf("symlinked hint: RootPath = %q", eff.RootPath)
	}
}

func TestForTargetUnresolvableRootIsError(t *testing.T) {
	cfg := Default()
	cfg.Roots = []Root{{Path: "$BROOOM_TEST_UNSET_VAR/x"}}
	if _, err := cfg.ForTarget("", t.TempDir()); err == nil || !strings.Contains(err.Error(), "roots[0].path") {
		t.Fatalf("got %v", err)
	}
}

func TestForTargetUnknownRootDetector(t *testing.T) {
	base, cfg := overlayFixture(t)
	cfg.Roots[0].Detectors = map[string]bool{"typo": false}
	if _, err := cfg.ForTarget("", filepath.Join(base, "ws", "repo")); err == nil || !strings.Contains(err.Error(), "typo") {
		t.Fatalf("got %v", err)
	}
}

func TestApplyRepoConfig(t *testing.T) {
	tests := []struct {
		name    string
		rc      *RepoConfig
		wantErr string
		check   func(*testing.T, *Config)
	}{
		{name: "nil is a copy", rc: nil, check: func(t *testing.T, c *Config) {
			if !reflect.DeepEqual(c, Default()) {
				t.Error("nil rc must not change anything")
			}
		}},
		{name: "disable known", rc: &RepoConfig{Disable: []string{"worktrees", "git-bloat"}}, check: func(t *testing.T, c *Config) {
			if c.Detectors.Worktrees.Enabled || c.Detectors.GitBloat.Enabled || !c.Detectors.StaleBranch.Enabled {
				t.Error("wrong detectors disabled")
			}
		}},
		{name: "disable unknown", rc: &RepoConfig{Disable: []string{"nope"}}, wantErr: `.brooom.json: disable: unknown detector "nope"`},
		{name: "lower min age", rc: &RepoConfig{Thresholds: &ThresholdOverrides{MinAgeDays: intp(7)}},
			wantErr: ".brooom.json: thresholds.min_age_days: 7 is lower than the effective value 14; repo config may only tighten"},
		{name: "lower min size", rc: &RepoConfig{Thresholds: &ThresholdOverrides{MinSizeBytes: int64p(-5)}},
			wantErr: "thresholds.min_size_bytes: -5 must not be negative"},
		{name: "negative recent", rc: &RepoConfig{Thresholds: &ThresholdOverrides{RecentDays: intp(-1)}},
			wantErr: "thresholds.recent_days"},
		{name: "lower recent", rc: &RepoConfig{Thresholds: &ThresholdOverrides{RecentDays: intp(1)}},
			wantErr: "thresholds.recent_days: 1 is lower than the effective value 2"},
		{name: "equal value accepted", rc: &RepoConfig{Thresholds: &ThresholdOverrides{MinAgeDays: intp(14), RecentDays: intp(2)}}},
		{name: "raise floors derived ages", rc: &RepoConfig{Thresholds: &ThresholdOverrides{MinAgeDays: intp(120)}}, check: func(t *testing.T, c *Config) {
			d := c.Detectors
			if c.Thresholds.MinAgeDays != 120 || d.StaleBranch.MinAgeDays != 120 || d.Worktrees.MinAgeDays != 120 {
				t.Errorf("derived ages not floored: %+v", d)
			}
		}},
		{name: "raise keeps higher derived age", rc: &RepoConfig{Thresholds: &ThresholdOverrides{MinAgeDays: intp(20)}}, check: func(t *testing.T, c *Config) {
			if c.Detectors.StaleBranch.MinAgeDays != 90 {
				t.Errorf("stale age lowered to %d", c.Detectors.StaleBranch.MinAgeDays)
			}
		}},
		{name: "raise floors pointer ages", rc: &RepoConfig{Thresholds: &ThresholdOverrides{MinAgeDays: intp(50)}}, check: func(t *testing.T, c *Config) {
			d := c.Detectors
			if *d.AIArtifacts.MinAgeDays != 50 || *d.Logs.MinAgeDays != 60 {
				t.Errorf("ai=%d logs=%d", *d.AIArtifacts.MinAgeDays, *d.Logs.MinAgeDays)
			}
		}},
		{name: "raise size floors large-untracked", rc: &RepoConfig{Thresholds: &ThresholdOverrides{MinSizeBytes: int64p(500 << 20)}}, check: func(t *testing.T, c *Config) {
			if c.Thresholds.MinSizeBytes != 500<<20 || c.Detectors.LargeUntracked.MinSizeBytes != 500<<20 {
				t.Errorf("size not floored: %+v", c.Detectors.LargeUntracked)
			}
		}},
		{name: "protected appended deduped in order", rc: &RepoConfig{ProtectedBranches: []string{"hotfix/*", "main", "hotfix/*", "prod"}}, check: func(t *testing.T, c *Config) {
			want := append(slices.Clone(Default().Git.ProtectedBranches), "hotfix/*", "prod")
			if !slices.Equal(c.Git.ProtectedBranches, want) {
				t.Errorf("got %v", c.Git.ProtectedBranches)
			}
		}},
		{name: "protected bad glob", rc: &RepoConfig{ProtectedBranches: []string{"[x"}}, wantErr: "protected_branches[0]"},
		{name: "exclude collected", rc: &RepoConfig{Exclude: []string{"gen", "**/tmp", "gen"}}, check: func(t *testing.T, c *Config) {
			if !slices.Equal(c.RepoExclude, []string{"gen", "**/tmp"}) {
				t.Errorf("got %v", c.RepoExclude)
			}
		}},
		{name: "exclude bad glob", rc: &RepoConfig{Exclude: []string{"ok", "[x"}}, wantErr: "exclude[1]"},
		{name: "version too new", rc: &RepoConfig{Version: 2}, wantErr: "version"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base := Default()
			four := 60
			base.Detectors.Logs.MinAgeDays = &four
			three := 5
			base.Detectors.AIArtifacts.MinAgeDays = &three
			if tt.name == "nil is a copy" {
				base = Default()
			}
			before := base.clone()
			eff, err := base.ApplyRepoConfig(tt.rc)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("want error containing %q, got %v", tt.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(base, before) {
				t.Error("receiver mutated")
			}
			if tt.check != nil {
				tt.check(t, eff)
			}
		})
	}
}

func writeRepoConfig(t *testing.T, repo, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, RepoConfigFileName), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadRepoConfig(t *testing.T) {
	tests := []struct {
		name, content, want string
	}{
		{"unknown key", `{"disabel":["worktrees"]}`, `unknown key "disabel" (did you mean "disable"?)`},
		{"cannot enable", `{"enable":["worktrees"]}`, `unknown key "enable"`},
		{"cannot touch trash", `{"trash":{"strategy":"delete"}}`, `unknown key "trash"`},
		{"cannot touch roots", `{"roots":[]}`, `unknown key "roots"`},
		{"cannot edit git", `{"git":{"protected_branches":[]}}`, `unknown key "git"`},
		{"threshold typo path", `{"thresholds":{"min_age":3}}`, `unknown key "thresholds.min_age"`},
		{"wrong type", `{"exclude":"gen"}`, "exclude: expected array, got string"},
		{"empty file", ``, "empty"},
		{"version too new", `{"version":2}`, "upgrade Brooom"},
		{"negative version", `{"version":-1}`, "invalid"},
		{"syntax", "{\n\"a\"", "line"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			writeRepoConfig(t, dir, tt.content)
			_, err := LoadRepoConfig(filepath.Join(dir, RepoConfigFileName))
			if err == nil || !strings.Contains(err.Error(), tt.want) || !strings.Contains(err.Error(), RepoConfigFileName) {
				t.Fatalf("want error naming file and containing %q, got %v", tt.want, err)
			}
		})
	}
}

func TestLoadRepoConfigAccepts(t *testing.T) {
	dir := t.TempDir()
	if rc, err := LoadRepoConfig(filepath.Join(dir, RepoConfigFileName)); rc != nil || err != nil {
		t.Fatalf("missing file: %v %v", rc, err)
	}
	for _, content := range []string{`{}`, `{"version":0}`, `{"version":1,"disable":["git-bloat"]}`, "\xEF\xBB\xBF{}"} {
		writeRepoConfig(t, dir, content)
		rc, err := LoadRepoConfig(filepath.Join(dir, RepoConfigFileName))
		if err != nil || rc == nil {
			t.Errorf("%q: %v", content, err)
		}
	}
}

func TestLoadRepoConfigRefusesNonRegular(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(t.TempDir(), "secret.json")
	if err := os.WriteFile(secret, []byte(`{"disable":["worktrees"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, RepoConfigFileName)
	if err := os.Symlink(secret, p); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := LoadRepoConfig(p); err == nil || !strings.Contains(err.Error(), "symlink") || !strings.Contains(err.Error(), p) {
		t.Fatalf("symlink must be refused, got %v", err)
	}
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(p, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadRepoConfig(p); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("directory must be refused, got %v", err)
	}
}

func TestLoadRepoConfigSizeCap(t *testing.T) {
	dir := t.TempDir()
	writeRepoConfig(t, dir, `{"exclude":["`+strings.Repeat("a", maxRepoConfigBytes)+`"]}`)
	_, err := LoadRepoConfig(filepath.Join(dir, RepoConfigFileName))
	if err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("got %v", err)
	}
}
