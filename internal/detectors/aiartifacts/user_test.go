package aiartifacts

import (
	"errors"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/scope"
)

// userHome builds a fake home:
//
//	~/.claude/projects/p3/s.jsonl          transcript (built-in wildcard entry)
//	~/.claude/settings.json                configuration, a sibling of the base
//	~/.myagent/sessions/s1/notes.txt       entries of a wildcard-free location
//	~/.myagent/sessions/s2/notes.txt
//	~/.myagent/sessions/s3/other.txt
//	~/.myagent/sessions/s4/notes.txt       too young
//	~/.wildagent/runs/r1/log.txt           wildcard location with protect rule
//	~/.wildagent/runs/r2/keep/k.txt        r2 holds protected data
func userHome(t *testing.T) string {
	t.Helper()
	home := sandbox(t)
	put(t, home, ".claude/projects/p3/s.jsonl", 100)
	put(t, home, ".claude/settings.json", 100)
	put(t, home, ".myagent/sessions/s1/notes.txt", 100)
	put(t, home, ".myagent/sessions/s2/notes.txt", 100)
	put(t, home, ".myagent/sessions/s3/other.txt", 100)
	put(t, home, ".myagent/sessions/s4/notes.txt", 3)
	put(t, home, ".wildagent/runs/r1/log.txt", 100)
	put(t, home, ".wildagent/runs/r2/keep/k.txt", 100)
	put(t, home, ".wildagent/runs/r2/log.txt", 100)
	oldDirs(t, home)
	return home
}

// userCfg adds two custom tools and toggles user locations. my-agent has the
// wildcard-free location ~/.myagent/sessions (findings are the entries inside
// it); wild-agent has the wildcard location ~/.wildagent/runs/* plus a protect
// rule for a directory inside such an entry. (The catalog itself refuses a
// wildcard-free entry that contains a protected path.)
func userCfg(enabled bool) *config.Config {
	cfg := config.Default()
	cfg.Detectors.AIArtifacts.UserLocations = enabled
	age := 30
	entry := func(pattern, desc string) []config.CatalogEntry {
		return []config.CatalogEntry{{
			Scope: "user", Patterns: []string{pattern}, Kind: "any",
			Confidence: "medium", MinAgeDays: &age, Description: desc,
		}}
	}
	cfg.Detectors.AIArtifacts.Extra = []config.CatalogTool{
		{ID: "my-agent", Name: "My Agent", Entries: entry("~/.myagent/sessions", "sessions of my agent")},
		{
			ID: "wild-agent", Name: "Wild Agent", Entries: entry("~/.wildagent/runs/*", "runs of the wild agent"),
			Protect: []config.CatalogProtect{{Scope: "user", Patterns: []string{"~/.wildagent/runs/*/keep"}, Reason: "notes worth keeping"}},
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
	// Exactly the existing bases, never a parent such as ~ or ~/.claude; the
	// other catalog locations do not exist in the fake home.
	var got []string
	for _, tg := range on {
		got = append(got, tg.Tool+"="+tg.Path)
	}
	want := []string{
		"claude-code=" + filepath.Join(home, ".claude", "projects"),
		"my-agent=" + filepath.Join(home, ".myagent", "sessions"),
		"wild-agent=" + filepath.Join(home, ".wildagent", "runs"),
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

func TestExtraTargetsIgnoreNonAITools(t *testing.T) {
	home := sandbox(t)
	// A dev-tool location that exists must not be declared by this detector.
	put(t, home, ".npm/_logs/x.log", 100)
	on, err := New().ExtraTargets(t.Context(), config.Default())
	if err != nil || len(on) != 0 {
		t.Errorf("targets = %+v, %v; want none", on, err)
	}
}

func TestUserFindingsAreEntriesNeverTheBase(t *testing.T) {
	home := userHome(t)
	cfg := userCfg(true)
	tg := targetFor(t, cfg, "my-agent")
	env := newEnv(t, cfg, tg.Path)

	before := snapshot(t, home)
	got := mustScan(t, env, tg)
	if !reflect.DeepEqual(snapshot(t, home), before) {
		t.Error("the scan modified the home directory")
	}

	// s1 to s3 are entries directly inside the base, s4 is too young, and the
	// base itself is never reported.
	if paths, want := relPaths(t, got, tg.Path), []string{"s1", "s2", "s3"}; !reflect.DeepEqual(paths, want) {
		t.Fatalf("paths = %v, want %v", paths, want)
	}
	for _, f := range got {
		if f.Path == tg.Path {
			t.Errorf("the base itself was reported")
		}
		if !f.HasRisk(findings.RiskOutsideRepo) || f.Scope.Type != findings.ScopeUser || f.Scope.Path != tg.Path ||
			f.Tool != "my-agent" || f.Kind != findings.KindDir {
			t.Errorf("finding = %+v", f)
		}
		if f.SuggestedAction.Type != findings.ActionTrash || f.SuggestedAction.Reason != "sessions of my agent" {
			t.Errorf("%s: action = %+v", f.Path, f.SuggestedAction)
		}
	}
}

// TestUserProtectWins: a wildcard entry whose directory contains a protected
// path is dropped, its sibling is reported.
func TestUserProtectWins(t *testing.T) {
	userHome(t)
	cfg := userCfg(true)
	tg := targetFor(t, cfg, "wild-agent")
	env := newEnv(t, cfg, tg.Path)
	got := relPaths(t, mustScan(t, env, tg), tg.Path)
	if want := []string{"r1"}; !slices.Equal(got, want) {
		t.Errorf("paths = %v, want %v", got, want)
	}
}

func TestUserWildcardTranscripts(t *testing.T) {
	home := sandbox(t)
	put(t, home, ".claude/projects/a/one.jsonl", 100)
	put(t, home, ".claude/projects/a/young.jsonl", 3)
	put(t, home, ".claude/projects/a/keep.txt", 100)
	put(t, home, ".claude/projects/b/two.jsonl", 100)
	put(t, home, ".claude/projects/b/memory/three.jsonl", 100)
	oldDirs(t, home)
	cfg := config.Default()
	cfg.Detectors.AIArtifacts.UserLocations = true
	tg := targetFor(t, cfg, "claude-code")
	env := newEnv(t, cfg, tg.Path)
	got := mustScan(t, env, tg)
	if paths, want := relPaths(t, got, tg.Path), []string{"a/one.jsonl", "b/two.jsonl"}; !slices.Equal(paths, want) {
		t.Errorf("paths = %v, want %v", paths, want)
	}
	if f := got[0]; f.Kind != findings.KindFile || f.Meta["pattern"] == "" || f.Tool != "claude-code" {
		t.Errorf("finding = %+v", f)
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
		filepath.Join(home, ".claude", "settings.json"),
		filepath.Join(home, ".claude"),
		home,
	} {
		if _, err := env.Guard.Resolve(refused); !errors.Is(err, scope.ErrOutsideScope) {
			t.Errorf("%s: err = %v, want ErrOutsideScope", refused, err)
		}
	}
	if _, err := env.Guard.Resolve(filepath.Join(home, ".claude", "projects", "p3")); err != nil {
		t.Errorf("entry inside the base: %v", err)
	}
	if !env.Guard.IsAllowedRoot(bases[0]) {
		t.Error("the base must be an allowed root, which actions never remove")
	}
}

func TestUserTargetsOfOtherToolsAreIgnored(t *testing.T) {
	userHome(t)
	cfg := userCfg(true)
	base := targetFor(t, cfg, "my-agent").Path
	env := newEnv(t, cfg, base)
	for _, tool := range []string{"npm", "no-such-tool", ""} {
		tg := scope.Target{Kind: scope.TargetUser, Path: base, Tool: tool, Scope: findings.Scope{Type: findings.ScopeUser, Path: base}}
		got, err := scan(t, env, tg)
		if err != nil || len(got) != 0 {
			t.Errorf("tool %q: got %d findings, err %v; want none", tool, len(got), err)
		}
	}
}

func TestUserTargetMissingPathIsAnError(t *testing.T) {
	home := userHome(t)
	cfg := userCfg(true)
	env := newEnv(t, cfg, targetFor(t, cfg, "my-agent").Path)
	gone := filepath.Join(home, ".claude", "file-history")
	tg := scope.Target{Kind: scope.TargetUser, Path: gone, Tool: "claude-code", Scope: findings.Scope{Type: findings.ScopeUser, Path: gone}}
	if _, err := scan(t, env, tg); err == nil {
		t.Error("want an error for a missing user location")
	}
}

func TestUserToolToggle(t *testing.T) {
	userHome(t)
	cfg := userCfg(true)
	cfg.Detectors.AIArtifacts.Tools = map[string]bool{"my-agent": false, "claude-code": false, "wild-agent": false}
	targets, err := New().ExtraTargets(t.Context(), cfg)
	if err != nil || len(targets) != 0 {
		t.Errorf("disabled tools declared targets: %v, %v", targets, err)
	}
}

func TestUserFileBaseYieldsNothing(t *testing.T) {
	home := sandbox(t)
	// A wildcard-free location that is a file: its only possible finding
	// would be the base itself, which is never reported.
	put(t, home, ".codex/log", 100)
	oldDirs(t, home)
	cfg := config.Default()
	cfg.Detectors.AIArtifacts.UserLocations = true
	tg := targetFor(t, cfg, "codex-cli")
	env := newEnv(t, cfg, home)
	if got := mustScan(t, env, tg); len(got) != 0 {
		t.Errorf("findings %v", relPaths(t, got, tg.Path))
	}
}
