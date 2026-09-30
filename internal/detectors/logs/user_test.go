package logs

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/scope"
)

// userHome builds a fake home:
//
//	~/.mytool/logs/s1/x.txt            entries of a wildcard-free location
//	~/.mytool/logs/s2/x.txt
//	~/.mytool/logs/s3.txt
//	~/.mytool/logs/s4/x.txt            too young
//	~/.mytool/settings.json            a sibling of the base
//	~/.wildtool/runs/r1/log.txt        wildcard location with a protect rule
//	~/.wildtool/runs/r2/keep/k.txt     r2 holds protected data
//	~/.claude/projects/p/s.jsonl       an ai location, not ours
func userHome(t *testing.T) string {
	t.Helper()
	home := sandbox(t)
	put(t, home, ".mytool/logs/s1/x.txt", 100)
	put(t, home, ".mytool/logs/s2/x.txt", 100)
	put(t, home, ".mytool/logs/s3.txt", 100)
	put(t, home, ".mytool/logs/s4/x.txt", 3)
	put(t, home, ".mytool/settings.json", 100)
	put(t, home, ".wildtool/runs/r1/log.txt", 100)
	put(t, home, ".wildtool/runs/r2/keep/k.txt", 100)
	put(t, home, ".wildtool/runs/r2/log.txt", 100)
	put(t, home, ".claude/projects/p/s.jsonl", 100)
	oldDirs(t, home)
	return home
}

// userCfg adds two custom tools and toggles user locations. my-tool has the
// wildcard-free location ~/.mytool/logs (findings are the entries inside it);
// wild-tool has the wildcard location ~/.wildtool/runs/* plus a protect rule
// for a directory inside such an entry.
func userCfg(enabled bool) *config.Config {
	cfg := config.Default()
	cfg.Detectors.Logs.UserLocations = enabled
	age := 30
	entry := func(pattern, desc string) []config.CatalogEntry {
		return []config.CatalogEntry{{
			Scope: "user", Patterns: []string{pattern}, Kind: "any",
			Confidence: "medium", MinAgeDays: &age, Description: desc,
		}}
	}
	cfg.Detectors.Logs.Extra = []config.CatalogTool{
		{ID: "my-tool", Name: "My Tool", Entries: entry("~/.mytool/logs", "logs of my tool")},
		{
			ID: "wild-tool", Name: "Wild Tool", Entries: entry("~/.wildtool/runs/*", "runs of the wild tool"),
			Protect: []config.CatalogProtect{{Scope: "user", Patterns: []string{"~/.wildtool/runs/*/keep"}, Reason: "notes worth keeping"}},
		},
	}
	return cfg
}

// targetFor returns the extra target of one tool.
func targetFor(t *testing.T, cfg *config.Config, tool string) scope.Target {
	t.Helper()
	targets, err := New().ExtraTargets(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, tg := range targets {
		if tg.Tool == tool {
			return tg
		}
	}
	t.Fatalf("no target for %s in %+v", tool, targets)
	return scope.Target{}
}

func TestDefaultConfigKeepsUserLocationsOff(t *testing.T) {
	if config.Default().Detectors.Logs.UserLocations {
		t.Error("user_locations must default to false")
	}
}

func TestExtraTargetsOnlyWhenEnabled(t *testing.T) {
	home := userHome(t)
	off, err := New().ExtraTargets(t.Context(), userCfg(false))
	if err != nil || len(off) != 0 {
		t.Fatalf("disabled: %v, %v", off, err)
	}
	on, err := New().ExtraTargets(t.Context(), userCfg(true))
	if err != nil {
		t.Fatal(err)
	}
	// Exactly the existing bases of non-ai tools, never a parent such as ~ or
	// ~/.mytool; the ~/.claude location belongs to ai-artifacts.
	var got []string
	for _, tg := range on {
		got = append(got, tg.Tool+"="+tg.Path)
	}
	want := []string{
		"my-tool=" + filepath.Join(home, ".mytool", "logs"),
		"wild-tool=" + filepath.Join(home, ".wildtool", "runs"),
	}
	if !slices.Equal(got, want) {
		t.Fatalf("targets = %v, want %v", got, want)
	}
	for _, tg := range on {
		if tg.Kind != scope.TargetUser || tg.Scope.Type != findings.ScopeUser || tg.Scope.Path != tg.Path {
			t.Errorf("target = %+v", tg)
		}
	}
}

// npmHome builds a fake home with the embedded npm locations, which exist on
// Linux and macOS under ~/.npm.
func npmHome(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the npm locations live below %LOCALAPPDATA% on Windows; covered by the custom-tool tests")
	}
	home := sandbox(t)
	put(t, home, ".npm/_cacache/content-v2/a", 100)
	put(t, home, ".npm/_cacache/index-v5/b", 100)
	put(t, home, ".npm/_logs/2020-01-01-debug.log", 100)
	oldDirs(t, home)
	return home
}

// userLogsCfg enables user locations with the embedded catalog only.
func userLogsCfg() *config.Config {
	cfg := config.Default()
	cfg.Detectors.Logs.UserLocations = true
	return cfg
}

// TestBuiltinWildcardFreeLocationYieldsChildren: the npm cache pattern equals
// its base, so only the entries inside it are findings.
func TestBuiltinWildcardFreeLocationYieldsChildren(t *testing.T) {
	npmHome(t)
	cfg := userLogsCfg()
	cache := targetFor(t, cfg, "npm-cache")
	got := mustScan(t, newEnv(t, cfg, cache.Path), cache)
	if paths, want := relPaths(t, got, cache.Path), []string{"content-v2", "index-v5"}; !reflect.DeepEqual(paths, want) {
		t.Fatalf("paths = %v, want %v", paths, want)
	}
	for _, f := range got {
		if f.Path == cache.Path || f.Confidence != findings.ConfidenceLow || f.Meta["category"] != "cache" || !hasFlag(f, findings.RiskOutsideRepo) {
			t.Errorf("finding = %+v", f)
		}
	}
}

func TestBuiltinWildcardLocation(t *testing.T) {
	npmHome(t)
	cfg := userLogsCfg()
	logs := targetFor(t, cfg, "npm")
	got := relPaths(t, mustScan(t, newEnv(t, cfg, logs.Path), logs), logs.Path)
	if !slices.Equal(got, []string{"2020-01-01-debug.log"}) {
		t.Errorf("npm _logs paths = %v", got)
	}
}

func TestBuiltinCategoryToggleDropsTargets(t *testing.T) {
	npmHome(t)
	cfg := userLogsCfg()
	cfg.Detectors.Logs.Categories = map[string]bool{"cache": false}
	targets, err := New().ExtraTargets(t.Context(), cfg)
	if err != nil || len(targets) == 0 {
		t.Fatalf("targets = %v, %v", targets, err)
	}
	for _, tg := range targets {
		if tg.Tool == "npm-cache" {
			t.Errorf("disabled category still declares %+v", tg)
		}
	}
}

func TestUserFindingsAreEntriesNeverTheBase(t *testing.T) {
	home := userHome(t)
	cfg := userCfg(true)
	tg := targetFor(t, cfg, "my-tool")
	env := newEnv(t, cfg, tg.Path)

	before := snapshot(t, home)
	got := mustScan(t, env, tg)
	if !reflect.DeepEqual(snapshot(t, home), before) {
		t.Error("the scan modified the home directory")
	}

	// s1, s2 and s3.txt are entries directly inside the base, s4 is too young
	// and the base itself is never reported.
	if paths, want := relPaths(t, got, tg.Path), []string{"s1", "s2", "s3.txt"}; !reflect.DeepEqual(paths, want) {
		t.Fatalf("paths = %v, want %v", paths, want)
	}
	for _, f := range got {
		checkMyToolFinding(t, f, tg)
	}
}

func checkMyToolFinding(t *testing.T, f findings.Finding, tg scope.Target) {
	t.Helper()
	if f.Path == tg.Path {
		t.Errorf("the base itself was reported")
	}
	if !f.HasRisk(findings.RiskOutsideRepo) || f.Scope.Type != findings.ScopeUser || f.Scope.Path != tg.Path || f.Tool != "my-tool" {
		t.Errorf("finding = %+v", f)
	}
	if f.SuggestedAction.Type != findings.ActionTrash || f.SuggestedAction.Reason != "logs of my tool" {
		t.Errorf("%s: action = %+v", f.Path, f.SuggestedAction)
	}
}

// TestUserProtectWins: a wildcard entry whose directory contains a protected
// path is dropped, its sibling is reported.
func TestUserProtectWins(t *testing.T) {
	userHome(t)
	cfg := userCfg(true)
	tg := targetFor(t, cfg, "wild-tool")
	env := newEnv(t, cfg, tg.Path)
	got := relPaths(t, mustScan(t, env, tg), tg.Path)
	if want := []string{"r1"}; !slices.Equal(got, want) {
		t.Errorf("paths = %v, want %v", got, want)
	}
}

// TestUserProtectWinsWithSymlinkedHome: HOME is a symlink to the real home, as
// with dotfile managers. Protected data must still be filtered whichever
// spelling the walk and the protect rules use.
func TestUserProtectWinsWithSymlinkedHome(t *testing.T) {
	real := userHome(t)
	link := filepath.Join(filepath.Dir(real), "homelink")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	t.Setenv("HOME", link)
	t.Setenv("USERPROFILE", link)
	cfg := userCfg(true)
	tg := targetFor(t, cfg, "wild-tool")
	env := newEnv(t, cfg, tg.Path)
	// Findings carry the guard-resolved spelling, so compare against the real
	// base rather than the symlinked one.
	got := relPaths(t, mustScan(t, env, tg), filepath.Join(real, ".wildtool", "runs"))
	if want := []string{"r1"}; !slices.Equal(got, want) {
		t.Errorf("paths = %v, want %v", got, want)
	}
}

func TestGuardRefusesSiblingOfUserBase(t *testing.T) {
	home := userHome(t)
	cfg := userCfg(true)
	targets, err := New().ExtraTargets(t.Context(), cfg)
	if err != nil || len(targets) == 0 {
		t.Fatalf("targets = %v, %v", targets, err)
	}
	var bases []string
	for _, tg := range targets {
		bases = append(bases, tg.Path)
	}
	env := newEnv(t, cfg, bases...)
	for _, refused := range []string{
		filepath.Join(home, ".mytool", "settings.json"),
		filepath.Join(home, ".mytool"),
		home,
	} {
		if _, err := env.Guard.Resolve(refused); !errors.Is(err, scope.ErrOutsideScope) {
			t.Errorf("%s: err = %v, want ErrOutsideScope", refused, err)
		}
	}
	if _, err := env.Guard.Resolve(filepath.Join(home, ".mytool", "logs", "s1")); err != nil {
		t.Errorf("entry inside the base: %v", err)
	}
	if !env.Guard.IsAllowedRoot(bases[0]) {
		t.Error("the base must be an allowed root, which actions never remove")
	}
}

// TestUserTargetsOfOtherToolsAreIgnored: ai tools (declared by ai-artifacts)
// and unknown tools produce nothing here, even for an existing base.
func TestUserTargetsOfOtherToolsAreIgnored(t *testing.T) {
	home := userHome(t)
	cfg := userCfg(true)
	base := targetFor(t, cfg, "my-tool").Path
	aiBase := filepath.Join(home, ".claude", "projects")
	env := newEnv(t, cfg, base, aiBase)
	for _, tc := range []struct{ tool, path string }{
		{"claude-code", aiBase}, {"no-such-tool", base}, {"", base},
	} {
		tg := scope.Target{Kind: scope.TargetUser, Path: tc.path, Tool: tc.tool, Scope: findings.Scope{Type: findings.ScopeUser, Path: tc.path}}
		got, err := scan(t, env, tg)
		if err != nil || len(got) != 0 {
			t.Errorf("tool %q: got %d findings, err %v; want none", tc.tool, len(got), err)
		}
	}
}

func TestUserTargetMissingPathIsAnError(t *testing.T) {
	home := userHome(t)
	cfg := userCfg(true)
	env := newEnv(t, cfg, targetFor(t, cfg, "my-tool").Path)
	gone := filepath.Join(home, ".mytool", "gone")
	tg := scope.Target{Kind: scope.TargetUser, Path: gone, Tool: "my-tool", Scope: findings.Scope{Type: findings.ScopeUser, Path: gone}}
	if _, err := scan(t, env, tg); err == nil {
		t.Error("want an error for a missing user location")
	}
}

func TestUserCategoryToggle(t *testing.T) {
	userHome(t)
	cfg := userCfg(true)
	cfg.Detectors.Logs.Categories = map[string]bool{"logs": false}
	targets, err := New().ExtraTargets(t.Context(), cfg)
	if err != nil || len(targets) != 0 {
		t.Errorf("disabled category declared targets: %v, %v", targets, err)
	}
}

func TestUserFileBaseYieldsNothing(t *testing.T) {
	home := sandbox(t)
	// A wildcard-free location that is a file: its only possible finding
	// would be the base itself, which is never reported.
	put(t, home, ".filetool/log", 100)
	oldDirs(t, home)
	cfg := config.Default()
	cfg.Detectors.Logs.UserLocations = true
	age := 0
	cfg.Detectors.Logs.Extra = []config.CatalogTool{{ID: "file-tool", Name: "File Tool", Entries: []config.CatalogEntry{{
		Scope: "user", Patterns: []string{"~/.filetool/log"}, Kind: "any", Confidence: "high", MinAgeDays: &age, Description: "d",
	}}}}
	tg := targetFor(t, cfg, "file-tool")
	env := newEnv(t, cfg, home)
	if got := mustScan(t, env, tg); len(got) != 0 {
		t.Errorf("findings %v", relPaths(t, got, tg.Path))
	}
}
