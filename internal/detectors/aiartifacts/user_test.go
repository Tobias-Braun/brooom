package aiartifacts

import (
	"errors"
	"path/filepath"
	"slices"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/scope"
)

// testRepo is the repository whose agent data the tests look for. It need
// not exist: only its encoded name matters.
var testRepo = filepath.FromSlash("/work/app")

// Claude Code's directory names for testRepo and neighbours, spelled out
// instead of computed so the encoding itself is pinned.
const (
	encRepo     = "-work-app"
	encWorktree = "-work-app--claude-worktrees-agent-1"
	encDotWT    = "-work-app--worktrees-feat"
	encSibling  = "-work-app-site" // a different repository sharing the prefix
	encOther    = "-work-other"
)

// repoHome builds a fake home with transcripts of testRepo, of two of its
// worktrees, of a lookalike sibling and of an unrelated repository:
//
//	~/.claude/projects/-work-app/one.jsonl                     reported
//	~/.claude/projects/-work-app/young.jsonl                   too young
//	~/.claude/projects/-work-app/keep.txt                      not a transcript
//	~/.claude/projects/-work-app/memory/m.jsonl                protected
//	~/.claude/projects/-work-app--claude-worktrees-agent-1/w.jsonl
//	~/.claude/projects/-work-app--worktrees-feat/f.jsonl
//	~/.claude/projects/-work-app-site/s.jsonl                  another repository
//	~/.claude/projects/-work-other/o.jsonl                     another repository
//	~/.claude/settings.json                                    configuration
func repoHome(t *testing.T) string {
	t.Helper()
	home := sandbox(t)
	for rel, age := range map[string]int{
		".claude/projects/" + encRepo + "/one.jsonl":      100,
		".claude/projects/" + encRepo + "/young.jsonl":    3,
		".claude/projects/" + encRepo + "/keep.txt":       100,
		".claude/projects/" + encRepo + "/memory/m.jsonl": 100,
		".claude/projects/" + encWorktree + "/w.jsonl":    100,
		".claude/projects/" + encDotWT + "/f.jsonl":       100,
		".claude/projects/" + encSibling + "/s.jsonl":     100,
		".claude/projects/" + encOther + "/o.jsonl":       100,
		".claude/settings.json":                           100,
	} {
		put(t, home, rel, age)
	}
	oldDirs(t, home)
	return home
}

// projectsDir is ~/.claude/projects of the fake home.
func projectsDir(home string) string { return filepath.Join(home, ".claude", "projects") }

// extraTargets returns the targets the detector declares for testRepo.
func extraTargets(t *testing.T, cfg *config.Config) []scope.Target {
	t.Helper()
	targets, err := New().ExtraTargets(t.Context(), cfg, []string{testRepo})
	if err != nil {
		t.Fatal(err)
	}
	return targets
}

// TestExtraTargetsAreTheRepositorySlice: exactly the directories of the
// repository and its worktrees, each one its own base, never the parent that
// holds every other repository and never a lookalike sibling.
func TestExtraTargetsAreTheRepositorySlice(t *testing.T) {
	home := repoHome(t)
	var got []string
	for _, tg := range extraTargets(t, config.Default()) {
		if tg.Kind != scope.TargetUser || tg.Tool != "claude-code" || tg.Scope != (findings.Scope{Type: findings.ScopeUser, Path: tg.Path}) {
			t.Errorf("target = %+v", tg)
		}
		got = append(got, tg.Path)
	}
	slices.Sort(got)
	want := []string{
		filepath.Join(projectsDir(home), encRepo),
		filepath.Join(projectsDir(home), encWorktree),
		filepath.Join(projectsDir(home), encDotWT),
	}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("targets = %v, want %v", got, want)
	}
}

func TestExtraTargetsNeedRepositories(t *testing.T) {
	repoHome(t)
	targets, err := New().ExtraTargets(t.Context(), config.Default(), nil)
	if err != nil || len(targets) != 0 {
		t.Fatalf("without repositories: %v, %v", targets, err)
	}
}

func TestExtraTargetsIgnoreNonAITools(t *testing.T) {
	home := sandbox(t)
	// A dev-tool location that exists must not be declared by this detector.
	put(t, home, ".npm/_logs/x.log", 100)
	if on := extraTargets(t, config.Default()); len(on) != 0 {
		t.Errorf("targets = %+v; want none", on)
	}
}

// TestRepositoryTranscripts scans the repository's own directory: old
// transcripts are reported as files, young ones, other files and the
// protected memory are not.
func TestRepositoryTranscripts(t *testing.T) {
	home := repoHome(t)
	base := filepath.Join(projectsDir(home), encRepo)
	cfg := config.Default()
	tg := scope.Target{Kind: scope.TargetUser, Path: base, Tool: "claude-code", Scope: findings.Scope{Type: findings.ScopeUser, Path: base}}
	env := newEnv(t, cfg, base)
	got := mustScan(t, env, tg)
	if paths, want := relPaths(t, got, base), []string{"one.jsonl"}; !slices.Equal(paths, want) {
		t.Fatalf("paths = %v, want %v", paths, want)
	}
	f := got[0]
	if f.Kind != findings.KindFile || f.Meta["pattern"] == "" || f.Tool != "claude-code" || !f.HasRisk(findings.RiskOutsideRepo) {
		t.Errorf("finding = %+v", f)
	}
	if f.SuggestedAction.Type != findings.ActionTrash {
		t.Errorf("action = %+v", f.SuggestedAction)
	}
}

// TestGuardRefusesOtherRepositories: with the declared bases as the only
// allowed user locations, the data of other repositories, the projects
// directory itself and Claude Code's settings stay out of scope.
func TestGuardRefusesOtherRepositories(t *testing.T) {
	home := repoHome(t)
	var bases []string
	for _, tg := range extraTargets(t, config.Default()) {
		bases = append(bases, tg.Path)
	}
	env := newEnv(t, config.Default(), bases...)
	for _, refused := range []string{
		filepath.Join(projectsDir(home), encSibling, "s.jsonl"),
		filepath.Join(projectsDir(home), encOther, "o.jsonl"),
		projectsDir(home),
		filepath.Join(home, ".claude", "settings.json"),
		home,
	} {
		if _, err := env.Guard.Resolve(refused); !errors.Is(err, scope.ErrOutsideScope) {
			t.Errorf("%s: err = %v, want ErrOutsideScope", refused, err)
		}
	}
	if _, err := env.Guard.Resolve(filepath.Join(projectsDir(home), encRepo, "one.jsonl")); err != nil {
		t.Errorf("transcript of the repository: %v", err)
	}
	for _, b := range bases {
		if !env.Guard.IsAllowedRoot(b) {
			t.Errorf("%s must be an allowed root, which actions never remove", b)
		}
	}
}

func TestUserTargetsOfOtherToolsAreIgnored(t *testing.T) {
	home := repoHome(t)
	base := filepath.Join(projectsDir(home), encRepo)
	env := newEnv(t, config.Default(), base)
	for _, tool := range []string{"npm", "no-such-tool", ""} {
		tg := scope.Target{Kind: scope.TargetUser, Path: base, Tool: tool, Scope: findings.Scope{Type: findings.ScopeUser, Path: base}}
		got, err := scan(t, env, tg)
		if err != nil || len(got) != 0 {
			t.Errorf("tool %q: got %d findings, err %v; want none", tool, len(got), err)
		}
	}
}

func TestUserTargetMissingPathIsAnError(t *testing.T) {
	home := repoHome(t)
	gone := filepath.Join(projectsDir(home), "-work-gone")
	env := newEnv(t, config.Default(), filepath.Join(projectsDir(home), encRepo))
	tg := scope.Target{Kind: scope.TargetUser, Path: gone, Tool: "claude-code", Scope: findings.Scope{Type: findings.ScopeUser, Path: gone}}
	if _, err := scan(t, env, tg); err == nil {
		t.Error("want an error for a missing user location")
	}
}

func TestUserToolToggle(t *testing.T) {
	repoHome(t)
	cfg := config.Default()
	cfg.Detectors.AIArtifacts.Tools = map[string]bool{"claude-code": false}
	if targets := extraTargets(t, cfg); len(targets) != 0 {
		t.Errorf("a disabled tool declared targets: %v", targets)
	}
}
