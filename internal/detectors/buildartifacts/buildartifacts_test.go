package buildartifacts

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/detect"
	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/scope"
	"github.com/Tobias-Braun/brooom/internal/testutil"
	"github.com/Tobias-Braun/brooom/internal/walk"
)

type wantFinding struct {
	tool string
	conf findings.Confidence
}

const (
	high   = findings.ConfidenceHigh
	medium = findings.ConfidenceMedium
	low    = findings.ConfidenceLow
)

// TestEcosystems runs synthetic projects of every ecosystem through the
// detector. All files are old, so the projects are inactive and confidence
// is decided by the markers and caps alone.
func TestEcosystems(t *testing.T) {
	tests := []struct {
		name  string
		files []string
		want  map[string]wantFinding
	}{
		{"node", []string{"package.json", "node_modules/a/index.js"}, map[string]wantFinding{"node_modules": {"node", high}}},
		{"rust", []string{"Cargo.toml", "target/debug/app"}, map[string]wantFinding{"target": {"rust", high}}},
		{"maven", []string{"pom.xml", "target/classes/A.class"}, map[string]wantFinding{"target": {"maven", high}}},
		{"target without marker", []string{"target/x"}, nil},
		{"venv with pyvenv.cfg", []string{"app.py", ".venv/pyvenv.cfg", ".venv/lib/x.py"}, map[string]wantFinding{".venv": {"python", high}}},
		{"venv without pyvenv.cfg", []string{".venv/lib/x.py", "venv/y", "env/z"}, nil},
		{"plain env folder", []string{"env/notes.txt", "setup.py"}, nil},
		{"gradle", []string{"build.gradle", "build/classes/A.class", ".gradle/x"}, map[string]wantFinding{"build": {"gradle", high}, ".gradle": {"gradle", high}}},
		{"gradle settings only", []string{"settings.gradle.kts", "build/x"}, map[string]wantFinding{"build": {"gradle", high}}},
		{"dotnet", []string{"App.csproj", "bin/Debug/a.dll", "obj/x.json"}, map[string]wantFinding{"bin": {"dotnet", high}, "obj": {"dotnet", high}}},
		{"dotnet without project", []string{"bin/x", "obj/y"}, nil},
		{"next", []string{"package.json", "next.config.js", ".next/cache/x"}, map[string]wantFinding{".next": {"node", high}}},
		{"next without config", []string{"package.json", ".next/x"}, nil},
		{"nuxt", []string{"nuxt.config.ts", ".nuxt/x", ".output/y"}, map[string]wantFinding{".nuxt": {"node", high}, ".output": {"node", high}}},
		{"svelte-kit", []string{"package.json", ".svelte-kit/x"}, map[string]wantFinding{".svelte-kit": {"node", high}}},
		{"angular cache", []string{"angular.json", ".angular/cache/x"}, map[string]wantFinding{".angular/cache": {"node", high}}},
		{"angular sibling kept", []string{"angular.json", ".angular/other/x"}, nil},
		{"pycache anywhere", []string{"a/b/app.py", "a/b/__pycache__/x.pyc"}, map[string]wantFinding{"a/b/__pycache__": {"python", high}}},
		{"egg-info", []string{"setup.py", "foo.egg-info/PKG-INFO"}, map[string]wantFinding{"foo.egg-info": {"python", high}}},
		{"tox", []string{"tox.ini", ".tox/py311/x"}, map[string]wantFinding{".tox": {"python", high}}},
		{"cocoapods", []string{"Podfile", "Pods/x"}, map[string]wantFinding{"Pods": {"cocoapods", high}}},
		{"dart", []string{"pubspec.yaml", ".dart_tool/x"}, map[string]wantFinding{".dart_tool": {"dart", high}}},
		{"elixir", []string{"mix.exs", "_build/x", "deps/y"}, map[string]wantFinding{"_build": {"elixir", high}, "deps": {"elixir", high}}},
		{"zig", []string{"build.zig", ".zig-cache/x", "zig-cache/y"}, map[string]wantFinding{".zig-cache": {"zig", high}, "zig-cache": {"zig", high}}},
		{"terraform capped at medium", []string{"main.tf", ".terraform/x"}, map[string]wantFinding{".terraform": {"terraform", medium}}},
		{"go vendor capped at low", []string{"go.mod", "go.sum", "vendor/x.go"}, map[string]wantFinding{"vendor": {"go", low}}},
		{"go vendor needs both markers", []string{"go.mod", "vendor/x.go"}, nil},
		{"composer vendor", []string{"composer.json", "composer.lock", "vendor/x.php"}, map[string]wantFinding{"vendor": {"php", low}}},
		{"dist without marker", []string{"dist/x.js", "build/y", "out/z"}, nil},
		{"dist with marker", []string{"package.json", "dist/x.js"}, map[string]wantFinding{"dist": {"generic", high}}},
		{"build with Makefile only", []string{"Makefile", "build/x"}, nil},
		{"out with cmake", []string{"CMakeLists.txt", "out/x"}, map[string]wantFinding{"out": {"generic", high}}},
		{"stray node_modules", []string{"lib/node_modules/a/index.js"}, map[string]wantFinding{"lib/node_modules": {"node", medium}}},
		{"nested artifacts reported once", []string{
			"package.json", "node_modules/foo/package.json", "node_modules/foo/node_modules/bar/x",
			".venv/pyvenv.cfg", ".venv/lib/__pycache__/x",
		}, map[string]wantFinding{"node_modules": {"node", high}, ".venv": {"python", high}}},
		{"monorepo", []string{
			"package.json", "node_modules/a/x",
			"packages/a/package.json", "packages/a/node_modules/x", "packages/a/dist/y",
			"packages/b/node_modules/x",
		}, map[string]wantFinding{
			"node_modules":            {"node", high},
			"packages/a/node_modules": {"node", high},
			"packages/a/dist":         {"generic", high},
			"packages/b/node_modules": {"node", medium},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, false)
			f.write(tt.files...)
			f.settle(f.daysAgo(100))
			got := f.byRel()
			var wantRels []string
			for rel := range tt.want {
				wantRels = append(wantRels, rel)
			}
			slices.Sort(wantRels)
			if !slices.Equal(rels(got), wantRels) {
				t.Fatalf("findings %v, want %v", rels(got), wantRels)
			}
			for rel, w := range tt.want {
				if got[rel].Tool != w.tool || got[rel].Confidence != w.conf {
					t.Errorf("%s: tool %q confidence %q, want %q %q", rel, got[rel].Tool, got[rel].Confidence, w.tool, w.conf)
				}
			}
		})
	}
}

func TestStrayNodeModulesEvidence(t *testing.T) {
	f := newFixture(t, false)
	f.write("lib/node_modules/a/index.js")
	f.settle(f.daysAgo(100))
	got := f.byRel()["lib/node_modules"]
	if !hasEvidence(got, "marker_missing") || hasEvidence(got, "marker") {
		t.Fatalf("evidence = %+v", got.Evidence)
	}
	if got.Confidence != medium {
		t.Fatalf("confidence = %s", got.Confidence)
	}
}

func TestFindingShape(t *testing.T) {
	f := newFixture(t, false)
	f.write("package.json", "node_modules/a/index.js")
	f.settle(f.daysAgo(100))
	all := f.run()
	if len(all) != 1 {
		t.Fatalf("findings = %+v", all)
	}
	got := all[0]
	want := filepath.Join(f.dir, "node_modules")
	if got.Path != want || got.Kind != findings.KindDir || got.Detector != Name || got.Tool != "node" {
		t.Fatalf("finding = %+v", got)
	}
	if got.ID != findings.NewID(Name, findings.KindDir, want, "") {
		t.Errorf("id = %s", got.ID)
	}
	if got.Scope.Path != f.dir {
		t.Errorf("scope = %+v", got.Scope)
	}
	if got.SizeBytes <= 0 || got.LastModified == nil || got.AgeDays != 100 {
		t.Errorf("size %d last %v age %d", got.SizeBytes, got.LastModified, got.AgeDays)
	}
	if got.SuggestedAction.Type != findings.ActionTrash || got.SuggestedAction.Command != "trash "+want || got.SuggestedAction.Reason == "" {
		t.Errorf("action = %+v", got.SuggestedAction)
	}
	for _, code := range []string{"matches_catalog", "ecosystem", "marker", "project_inactive_days"} {
		if !hasEvidence(got, code) {
			t.Errorf("missing evidence %s in %+v", code, got.Evidence)
		}
	}
	if m, _ := evidenceOf(got, "marker"); m.Value != "package.json" {
		t.Errorf("marker value = %v", m.Value)
	}
	if got.RiskFlags == nil || len(got.RiskFlags) != 0 {
		t.Errorf("risk flags = %#v, want empty non-nil", got.RiskFlags)
	}
	if hasEvidence(got, "gitignored") || hasEvidence(got, "not_gitignored") || hasEvidence(got, "last_commit") {
		t.Errorf("git evidence on a non-git project: %+v", got.Evidence)
	}
}

func TestEmissionOrderIsSorted(t *testing.T) {
	f := newFixture(t, false)
	f.write("package.json", "node_modules/x", "z/package.json", "z/node_modules/x", "a/package.json", "a/node_modules/x")
	f.settle(f.daysAgo(100))
	var paths []string
	for _, x := range f.run() {
		paths = append(paths, x.Path)
	}
	if !slices.IsSorted(paths) || len(paths) != 3 {
		t.Fatalf("paths = %v", paths)
	}
}

func TestProjectActivity(t *testing.T) {
	tests := []struct {
		name       string
		setup      func(f *fixture)
		conf       findings.Confidence
		recent     bool
		evidence   string
		noEvidence string
	}{
		{"inactive by commit and files", func(f *fixture) {}, high, false, "project_inactive_days", "project_active"},
		{"active by recent commit", func(f *fixture) {
			testutil.WriteFile(f.t, f.dir, "package.json", "changed")
			f.commit(f.daysAgo(5))
			f.settle(f.daysAgo(100))
		}, medium, true, "project_active", "project_inactive_days"},
		{"active by recent source file", func(f *fixture) {
			f.write("src/app.js")
			f.settle(f.daysAgo(100))
			f.touch("src/app.js", f.daysAgo(3))
		}, medium, true, "project_active", "project_inactive_days"},
		{"generated files never count", func(f *fixture) {
			f.write("node_modules/a/new.js")
			f.settle(f.daysAgo(100))
			f.touch("node_modules/a/new.js", f.daysAgo(10))
		}, high, false, "project_inactive_days", "project_active"},
		{"artifact changed within recent_days", func(f *fixture) {
			f.write("node_modules/a/new.js")
			f.settle(f.daysAgo(100))
			f.touch("node_modules/a/new.js", f.daysAgo(1))
		}, high, true, "project_inactive_days", "project_active"},
		{"activity exactly at the threshold is inactive", func(f *fixture) {
			f.write("src/app.js")
			f.settle(f.daysAgo(100))
			f.touch("src/app.js", f.daysAgo(30))
		}, high, false, "project_inactive_days", "project_active"},
		{"activity one day inside the threshold is active", func(f *fixture) {
			f.write("src/app.js")
			f.settle(f.daysAgo(100))
			f.touch("src/app.js", f.daysAgo(29))
		}, medium, true, "project_active", "project_inactive_days"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, true)
			f.write("package.json", "node_modules/a/index.js")
			f.repo.WriteFile(".gitignore", "node_modules/\n")
			f.commit(f.daysAgo(100))
			f.settle(f.daysAgo(100))
			tt.setup(f)
			got, ok := f.byRel()["node_modules"]
			if !ok {
				t.Fatal("no finding")
			}
			if got.Confidence != tt.conf || got.HasRisk(findings.RiskRecentlyModified) != tt.recent {
				t.Errorf("confidence %s recent %v, want %s %v", got.Confidence, got.HasRisk(findings.RiskRecentlyModified), tt.conf, tt.recent)
			}
			if !hasEvidence(got, tt.evidence) || hasEvidence(got, tt.noEvidence) {
				t.Errorf("evidence = %+v", got.Evidence)
			}
			if !hasEvidence(got, "last_commit") {
				t.Errorf("last_commit missing: %+v", got.Evidence)
			}
			if got.SuggestedAction.Type != findings.ActionTrash {
				t.Errorf("recently modified findings stay suggestible, got %+v", got.SuggestedAction)
			}
		})
	}
}

func TestActiveProjectValueIsDays(t *testing.T) {
	f := newFixture(t, false)
	f.write("src/app.js", "package.json", "node_modules/x")
	f.settle(f.daysAgo(100))
	f.touch("src/app.js", f.daysAgo(7))
	e, ok := evidenceOf(f.byRel()["node_modules"], "project_active")
	if !ok || e.Value != 7 {
		t.Fatalf("evidence = %+v", e)
	}
}

func TestInactiveDaysIsConfigurable(t *testing.T) {
	f := newFixture(t, false)
	f.write("package.json", "node_modules/x")
	f.settle(f.daysAgo(10))
	if got := f.byRel()["node_modules"]; got.Confidence != medium {
		t.Fatalf("default 30 days: confidence = %s", got.Confidence)
	}
	f.cfg.Detectors.BuildArtifacts.InactiveDays = 5
	if got := f.byRel()["node_modules"]; got.Confidence != high {
		t.Fatalf("5 days: confidence = %s", got.Confidence)
	}
}

func TestActiveProjectCapAtLow(t *testing.T) {
	f := newFixture(t, false)
	f.write("go.mod", "go.sum", "vendor/x.go")
	f.settle(f.daysAgo(1))
	if got := f.byRel()["vendor"]; got.Confidence != low || !got.HasRisk(findings.RiskRecentlyModified) {
		t.Fatalf("finding = %+v", got)
	}
}

func TestProjectWithoutAnySignalIsMedium(t *testing.T) {
	f := newFixture(t, false)
	f.write("__pycache__/x.pyc")
	f.settle(f.daysAgo(100))
	got := f.byRel()["__pycache__"]
	if got.Confidence != medium || !hasEvidence(got, "project_activity_unknown") || got.HasRisk(findings.RiskRecentlyModified) {
		t.Fatalf("finding = %+v", got)
	}
}

func TestRepositoryWithoutCommitsUsesFileTimes(t *testing.T) {
	f := newFixture(t, false)
	f.gitInit()
	f.write("package.json", "node_modules/x")
	f.settle(f.daysAgo(100))
	tgt := f.target()
	tgt.Kind = scope.TargetRepo
	out, err := f.runWith(context.Background(), f.env(), tgt)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Confidence != high || hasEvidence(out[0], "last_commit") {
		t.Fatalf("findings = %+v", out)
	}
	if out[0].HasRisk(findings.RiskTrackedFiles) {
		t.Fatal("nothing is tracked in an empty repository")
	}
}

func TestMonorepoActivityIsPerProject(t *testing.T) {
	f := newFixture(t, false)
	f.write("packages/a/package.json", "packages/a/node_modules/x", "packages/b/package.json", "packages/b/node_modules/x")
	f.settle(f.daysAgo(100))
	f.touch("packages/b/package.json", f.daysAgo(2))
	got := f.byRel()
	if got["packages/a/node_modules"].Confidence != high || got["packages/b/node_modules"].Confidence != medium {
		t.Fatalf("a %s b %s", got["packages/a/node_modules"].Confidence, got["packages/b/node_modules"].Confidence)
	}
}

func TestTrackedFiles(t *testing.T) {
	setup := func(f *fixture) {
		f.write("package.json", "dist/app.js", "node_modules/a/x")
		f.repo.WriteFile(".gitignore", "node_modules/\n")
		f.commit(f.daysAgo(100))
		f.settle(f.daysAgo(100))
	}
	t.Run("committed dist is blocked", func(t *testing.T) {
		f := newFixture(t, true)
		setup(f)
		got := f.byRel()["dist"]
		if !got.HasRisk(findings.RiskTrackedFiles) || got.SuggestedAction.Type != findings.ActionNone {
			t.Fatalf("finding = %+v", got)
		}
		if got.SuggestedAction.Reason == "" || got.SuggestedAction.Command != "" {
			t.Errorf("action = %+v", got.SuggestedAction)
		}
		if got.Confidence != high || !got.Blocked() {
			t.Errorf("confidence stays as computed: %+v", got)
		}
		if !hasEvidence(got, "tracked_files") {
			t.Errorf("evidence = %+v", got.Evidence)
		}
	})
	t.Run("force suggests trash with the forced reason", func(t *testing.T) {
		f := newFixture(t, true)
		setup(f)
		f.force = true
		got := f.byRel()["dist"]
		if !got.HasRisk(findings.RiskTrackedFiles) || got.Confidence != high {
			t.Fatalf("flag and confidence stay: %+v", got)
		}
		if got.SuggestedAction.Type != findings.ActionTrash || got.SuggestedAction.Reason != "forced: contains files tracked by git" {
			t.Fatalf("action = %+v", got.SuggestedAction)
		}
	})
	t.Run("untracked sibling stays actionable under force", func(t *testing.T) {
		f := newFixture(t, true)
		setup(f)
		f.force = true
		got := f.byRel()["node_modules"]
		if got.HasRisk(findings.RiskTrackedFiles) || got.SuggestedAction.Reason == "forced: contains files tracked by git" {
			t.Fatalf("finding = %+v", got)
		}
	})
	t.Run("one tracked file is enough", func(t *testing.T) {
		f := newFixture(t, true)
		f.write("package.json", "node_modules/keep.txt")
		f.commit(f.daysAgo(100))
		f.write("node_modules/new.txt")
		f.settle(f.daysAgo(100))
		got := f.byRel()["node_modules"]
		if !got.HasRisk(findings.RiskTrackedFiles) || got.SuggestedAction.Type != findings.ActionNone {
			t.Fatalf("finding = %+v", got)
		}
	})
}

func TestTrackedNameWithGlobCharactersIsLiteral(t *testing.T) {
	f := newFixture(t, true)
	f.write("package.json", "x.egg-info/PKG", "setup.py")
	f.write("y.egg-info/PKG")
	f.repo.Git("add", "x.egg-info", "setup.py", "package.json")
	f.repo.GitAt(f.daysAgo(100), "commit", "-q", "-m", "partial")
	f.settle(f.daysAgo(100))
	got := f.byRel()
	if !got["x.egg-info"].HasRisk(findings.RiskTrackedFiles) || got["y.egg-info"].HasRisk(findings.RiskTrackedFiles) {
		t.Fatalf("x tracked %v, y tracked %v", got["x.egg-info"].RiskFlags, got["y.egg-info"].RiskFlags)
	}
}

func TestGitignoreState(t *testing.T) {
	f := newFixture(t, true)
	f.repo.WriteFile(".gitignore", "dist/\n")
	f.write("package.json", "dist/app.js", "build/app.js")
	f.repo.Git("add", ".gitignore", "package.json")
	f.repo.GitAt(f.daysAgo(100), "commit", "-q", "-m", "base")
	f.settle(f.daysAgo(100))
	got := f.byRel()
	ignored, plain := got["dist"], got["build"]
	if !ignored.HasRisk(findings.RiskGitignored) || !hasEvidence(ignored, "gitignored") || hasEvidence(ignored, "not_gitignored") {
		t.Errorf("dist = %+v", ignored)
	}
	if ignored.HasRisk(findings.RiskTrackedFiles) || ignored.SuggestedAction.Type != findings.ActionTrash {
		t.Errorf("ignored dist stays actionable: %+v", ignored)
	}
	if plain.HasRisk(findings.RiskGitignored) || !hasEvidence(plain, "not_gitignored") || hasEvidence(plain, "gitignored") {
		t.Errorf("build = %+v", plain)
	}
	if len(plain.RiskFlags) != 0 {
		t.Errorf("a plain untracked artifact gets no extra flag: %v", plain.RiskFlags)
	}
}

func TestNestedRepositoriesAreNotWalked(t *testing.T) {
	f := newFixture(t, true)
	f.write("package.json", "node_modules/x")
	f.write("sub/package.json", "sub/node_modules/x")
	if err := os.MkdirAll(filepath.Join(f.dir, "sub", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A submodule or linked worktree has a .git file instead of a directory.
	f.write("mod/package.json", "mod/node_modules/x", "mod/.git")
	f.write("deep/inner/package.json", "deep/inner/node_modules/x")
	f.write("deep/keep/package.json", "deep/keep/node_modules/x", "deep/inner/.git")
	f.settle(f.daysAgo(100))
	got := rels(f.byRel())
	want := []string{"deep/keep/node_modules", "node_modules"}
	if !slices.Equal(got, want) {
		t.Fatalf("findings %v, want %v", got, want)
	}
}

func TestTargetRootIsNeverNested(t *testing.T) {
	f := newFixture(t, true)
	if _, err := os.Stat(filepath.Join(f.dir, ".git")); err != nil {
		t.Fatal(err)
	}
	f.write("package.json", "node_modules/x")
	f.settle(f.daysAgo(100))
	if got := f.byRel(); len(got) != 1 {
		t.Fatalf("findings = %v", rels(got))
	}
}

func TestExcludesPruneSubtrees(t *testing.T) {
	tree := []string{
		"keep/package.json", "keep/node_modules/x",
		"legacy/package.json", "legacy/node_modules/x",
		"nested/legacy/package.json", "nested/legacy/node_modules/x",
		"scratch/package.json", "scratch/node_modules/x",
	}
	tests := []struct {
		name  string
		setup func(f *fixture)
		want  []string
	}{
		{"root exclude relative to the target root", func(f *fixture) {
			f.cfg.Roots = []config.Root{{Path: f.dir, Exclude: []string{"legacy", "scratch"}}}
		}, []string{"keep/node_modules"}},
		{"root exclude relative to a parent root", func(f *fixture) {
			f.cfg.Roots = []config.Root{{Path: filepath.Dir(f.dir), Exclude: []string{filepath.Base(f.dir) + "/scratch", "**/legacy"}}}
		}, []string{"keep/node_modules"}},
		{"repo exclude from .brooom.json", func(f *fixture) {
			testutil.WriteFile(f.t, f.dir, ".brooom.json", `{"version":1,"exclude":["scratch","nested/legacy"]}`)
		}, []string{"keep/node_modules", "legacy/node_modules"}},
		{"no exclude", func(f *fixture) {}, []string{"keep/node_modules", "legacy/node_modules", "nested/legacy/node_modules", "scratch/node_modules"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, false)
			f.write(tree...)
			f.settle(f.daysAgo(100))
			tt.setup(f)
			slices.Sort(tt.want)
			if got := rels(f.byRel()); !slices.Equal(got, tt.want) {
				t.Fatalf("findings %v, want %v", got, tt.want)
			}
		})
	}
}

func TestExcludedSubtreeDoesNotCountAsActivity(t *testing.T) {
	f := newFixture(t, false)
	f.write("package.json", "node_modules/x", "legacy/new.js")
	f.settle(f.daysAgo(100))
	f.touch("legacy/new.js", f.daysAgo(1))
	f.cfg.Roots = []config.Root{{Path: f.dir, Exclude: []string{"legacy"}}}
	if got := f.byRel()["node_modules"]; got.Confidence != high {
		t.Fatalf("excluded files are not looked at: %+v", got)
	}
}

func TestSkipDirsAreNotWalked(t *testing.T) {
	f := newFixture(t, false)
	f.write("a/package.json", "a/node_modules/x", "vendored/package.json", "vendored/node_modules/x")
	f.settle(f.daysAgo(100))
	f.cfg.Scan.SkipDirs = []string{"vendored"}
	if got := rels(f.byRel()); !slices.Equal(got, []string{"a/node_modules"}) {
		t.Fatalf("findings = %v", got)
	}
}

func TestNonGitProject(t *testing.T) {
	f := newFixture(t, false)
	f.write("package.json", "node_modules/x", "dist/y")
	f.settle(f.daysAgo(100))
	for rel, got := range f.byRel() {
		if got.HasRisk(findings.RiskTrackedFiles) || got.HasRisk(findings.RiskGitignored) || got.Confidence != high {
			t.Errorf("%s = %+v", rel, got)
		}
		if got.SuggestedAction.Type != findings.ActionTrash {
			t.Errorf("%s action = %+v", rel, got.SuggestedAction)
		}
	}
}

func TestConfiguredDirs(t *testing.T) {
	tests := []struct {
		name  string
		tree  []string
		dirs  []string
		extra []string
		want  map[string]string // rel -> tool
	}{
		{"defaults", []string{"package.json", "node_modules/x", "dist/y", "gen/z"}, nil, nil, map[string]string{"node_modules": "node", "dist": "generic"}},
		{"dirs replace the list", []string{"package.json", "node_modules/x", "dist/y"}, []string{"dist"}, nil, map[string]string{"dist": "generic"}},
		{"dirs keep catalog markers for known names", []string{"node_modules/x", "dist/y"}, []string{"dist"}, nil, nil},
		{"dirs with unknown name is marker-less", []string{"gen/z", "node_modules/x", "package.json"}, []string{"gen"}, nil, map[string]string{"gen": "custom"}},
		{"dirs with markers", []string{"Makefile", "gen/z", "other/gen/q"}, []string{"gen:Makefile"}, nil, map[string]string{"gen": "custom"}},
		{"extra name is marker-less", []string{"gen/z", "sub/gen/q"}, nil, []string{"gen"}, map[string]string{"gen": "custom", "sub/gen": "custom"}},
		{"extra name overrides the catalog gate", []string{"dist/y"}, nil, []string{"dist"}, map[string]string{"dist": "custom"}},
		{"extra with markers is gated", []string{"Makefile", "cache2/z", "sub/cache2/q"}, nil, []string{"cache2:Makefile"}, map[string]string{"cache2": "custom"}},
		{"extra glob", []string{"a.gen-out/x"}, nil, []string{"*.gen-out"}, map[string]string{"a.gen-out": "custom"}},
		{"extras add to the catalog", []string{"package.json", "node_modules/x", "gen/z"}, nil, []string{"gen"}, map[string]string{"node_modules": "node", "gen": "custom"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, false)
			f.write(tt.tree...)
			f.settle(f.daysAgo(100))
			f.cfg.Detectors.BuildArtifacts.Dirs = tt.dirs
			f.cfg.Detectors.BuildArtifacts.ExtraDirs = tt.extra
			got := f.byRel()
			if len(got) != len(tt.want) {
				t.Fatalf("findings %v, want %v", rels(got), tt.want)
			}
			for rel, tool := range tt.want {
				if got[rel].Tool != tool {
					t.Errorf("%s tool = %q, want %q", rel, got[rel].Tool, tool)
				}
			}
		})
	}
}

func TestInvalidConfiguredDirsFailTheTarget(t *testing.T) {
	for _, field := range []string{"dirs", "extra_dirs"} {
		f := newFixture(t, false)
		f.write("package.json", "node_modules/x")
		if field == "dirs" {
			f.cfg.Detectors.BuildArtifacts.Dirs = []string{"a/b/c"}
		} else {
			f.cfg.Detectors.BuildArtifacts.ExtraDirs = []string{"x:"}
		}
		if _, err := f.runWith(context.Background(), f.env(), f.target()); err == nil {
			t.Errorf("%s: want an error", field)
		}
	}
}

func TestSymlinkedArtifactIsReportedNotFollowed(t *testing.T) {
	f := newFixture(t, false)
	f.write("package.json", "store/pkg/index.js", ".venv/pyvenv.cfg")
	outside := testutil.ResolvedTempDir(t)
	testutil.WriteFile(t, outside, "big.bin", "0123456789")
	if err := os.Symlink(filepath.Join(f.dir, "store"), filepath.Join(f.dir, "node_modules")); err != nil {
		t.Skip("cannot create symlinks:", err)
	}
	if err := os.Symlink(outside, filepath.Join(f.dir, ".turbo")); err != nil {
		t.Skip("cannot create symlinks:", err)
	}
	if err := os.Symlink(filepath.Join(f.dir, ".venv"), filepath.Join(f.dir, "venv")); err != nil {
		t.Skip("cannot create symlinks:", err)
	}
	f.settle(f.daysAgo(100))
	got := f.byRel()
	if !slices.Equal(rels(got), []string{".turbo", ".venv", "node_modules"}) {
		t.Fatalf("findings = %v (a symlinked venv is never inspected)", rels(got))
	}
	for _, rel := range []string{"node_modules", ".turbo"} {
		x := got[rel]
		if !x.HasRisk(findings.RiskSymlink) || x.SizeBytes != 0 {
			t.Errorf("%s: flags %v size %d", rel, x.RiskFlags, x.SizeBytes)
		}
		if x.Path != filepath.Join(f.dir, rel) {
			t.Errorf("%s: path %s must be the link itself", rel, x.Path)
		}
	}
	if got[".venv"].HasRisk(findings.RiskSymlink) {
		t.Error("the real venv is not a symlink")
	}
	if _, err := os.Stat(filepath.Join(outside, "big.bin")); err != nil {
		t.Fatal("detector must not touch the link target:", err)
	}
}

func TestSymlinkToFileIsIgnored(t *testing.T) {
	f := newFixture(t, false)
	f.write("package.json", "real.txt")
	if err := os.Symlink(filepath.Join(f.dir, "real.txt"), filepath.Join(f.dir, "node_modules")); err != nil {
		t.Skip("cannot create symlinks:", err)
	}
	if got := f.byRel(); len(got) != 0 {
		t.Fatalf("findings = %v", rels(got))
	}
}

func TestSizeThreshold(t *testing.T) {
	f := newFixture(t, false)
	f.write("package.json", "node_modules/x")
	f.write("a/package.json")
	if err := os.MkdirAll(filepath.Join(f.dir, "a", "node_modules"), 0o755); err != nil {
		t.Fatal(err)
	}
	f.settle(f.daysAgo(100))
	if got := rels(f.byRel()); !slices.Equal(got, []string{"a/node_modules", "node_modules"}) {
		t.Fatalf("without threshold: %v", got)
	}
	// An empty directory still occupies its own block(s), so it is not below
	// a 1 byte threshold any more; the threshold that separates the two
	// candidates is one byte above the empty directory's allocation.
	empty, err := walk.DirSize(context.Background(), filepath.Join(f.dir, "a", "node_modules"), walk.Options{Fresh: true})
	if err != nil {
		t.Fatal(err)
	}
	f.cfg.Thresholds.MinSizeBytes = empty.SizeBytes + 1
	if got := rels(f.byRel()); !slices.Equal(got, []string{"node_modules"}) {
		t.Fatalf("above the empty directory's %d bytes: %v", empty.SizeBytes, got)
	}
	f.cfg.Thresholds.MinSizeBytes = 1 << 40
	if got := f.byRel(); len(got) != 0 {
		t.Fatalf("with 1 TiB: %v", rels(got))
	}
}

func TestDisabledAndUserTargets(t *testing.T) {
	f := newFixture(t, false)
	f.write("package.json", "node_modules/x")
	f.settle(f.daysAgo(100))
	f.cfg.Detectors.BuildArtifacts.Enabled = false
	if got := f.byRel(); len(got) != 0 {
		t.Fatalf("disabled: %v", rels(got))
	}
	f.cfg.Detectors.BuildArtifacts.Enabled = true
	f.cfg.Roots = []config.Root{{Path: f.dir, Detectors: map[string]bool{Name: false}}}
	if got := f.byRel(); len(got) != 0 {
		t.Fatalf("disabled for the root: %v", rels(got))
	}
	f.cfg.Roots = nil
	tgt := f.target()
	tgt.Kind = scope.TargetUser
	out, err := f.runWith(context.Background(), f.env(), tgt)
	if err != nil || len(out) != 0 {
		t.Fatalf("user target: %v %v", out, err)
	}
}

func TestTargetOutsideGuardIsRefused(t *testing.T) {
	f := newFixture(t, false)
	f.write("package.json", "node_modules/x")
	other := testutil.ResolvedTempDir(t)
	guard, err := scope.NewGuard(other)
	if err != nil {
		t.Fatal(err)
	}
	env := &detect.Env{Config: f.cfg, Git: f.runner, Guard: guard, Now: f.now}
	if _, err := f.runWith(context.Background(), env, f.target()); err == nil {
		t.Fatal("want an error for a target outside the guard")
	}
}

func TestCancelledContext(t *testing.T) {
	f := newFixture(t, false)
	f.write("package.json", "node_modules/x")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.runWith(ctx, f.env(), f.target()); err == nil {
		t.Fatal("want the context error")
	}
}

func TestRegistered(t *testing.T) {
	d, ok := detect.Get(Name)
	if !ok {
		t.Fatal("detector not registered")
	}
	if d.Category() != detect.CategoryArtifacts || d.Name() != "build-artifacts" || d.Description() == "" {
		t.Fatalf("detector = %s %s", d.Name(), d.Category())
	}
}
