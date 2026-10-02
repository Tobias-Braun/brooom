package catalog

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// devCatalog loads the embedded catalog for one OS; the tests below exercise
// the real dev_tools.json, not a fixture, because over-matching in that data
// is the risk this file guards against.
func devCatalog(t *testing.T, goos string) *Catalog {
	t.Helper()
	c, err := Load(Options{GOOS: goos})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// TestDevToolsPolicy pins the confidence and age policy of docs/catalog.md
// and the exclusions that belong to other catalogs.
func TestDevToolsPolicy(t *testing.T) {
	c := devCatalog(t, "linux")
	for _, tool := range c.Tools() {
		if tool.Category == CategoryAI {
			continue
		}
		for i, e := range tool.Entries {
			checkEntry(t, fmt.Sprintf("%s entries[%d]", tool.ID, i), tool.Category, e)
		}
	}
	for _, id := range []string{"tox", "parcel", "vitest"} {
		if _, ok := c.Tool(id); ok {
			t.Errorf("tool %q belongs to the build-artifacts catalog", id)
		}
	}
}

// excludedPatterns are patterns that must never appear in this catalog: plain
// logs are meaningful, the rest is owned by the build-artifacts catalog or
// would blanket-match crash reports of unrelated apps.
var excludedPatterns = []string{"*.log", ".tox", ".parcel-cache", "node_modules/.vite/vitest", "*.ips", "**/*.ips"}

func checkEntry(t *testing.T, label string, cat Category, e Entry) {
	t.Helper()
	if e.Source == "" || e.Description == "" || e.MinAgeDays == nil {
		t.Errorf("%s: source, description and explicit min_age_days are required", label)
		return
	}
	checkPolicy(t, label, cat, e)
	for _, p := range e.Patterns {
		if slices.Contains(excludedPatterns, p) {
			t.Errorf("%s: pattern %q is deliberately excluded", label, p)
		}
	}
}

func checkPolicy(t *testing.T, label string, cat Category, e Entry) {
	t.Helper()
	age := *e.MinAgeDays
	switch {
	case cat == CategoryOSJunk && age != 0:
		t.Errorf("%s: os-junk min_age_days = %d, want 0", label, age)
	case cat != CategoryOSJunk && age != 14:
		t.Errorf("%s: min_age_days = %d, want 14", label, age)
	}
	if cat == CategoryCrash && e.Confidence != ConfidenceMedium {
		t.Errorf("%s: crash dumps are medium confidence, got %s", label, e.Confidence)
	}
	if e.Scope == ScopeProject && e.Kind == KindDir && cat == CategoryOSJunk {
		t.Errorf("%s: os-junk entries are files", label)
	}
}

func TestDevToolsProjectMatches(t *testing.T) {
	tests := []struct {
		name  string
		goos  string
		rel   string
		isDir bool
		tool  string // empty means no match
		conf  Confidence
	}{
		{name: "npm log", rel: "npm-debug.log", tool: "npm", conf: ConfidenceHigh},
		{name: "npm rotated numbered log", rel: "npm-debug.log.0", tool: "npm"},
		{name: "yarn error nested", rel: "sub/pkg/yarn-error.log", tool: "yarn"},
		{name: "yarn debug", rel: "yarn-debug.log", tool: "yarn"},
		{name: "yarn debug rotated is caught by the rotated entry", rel: "yarn-debug.log.1", tool: "rotated-logs"},
		{name: "pnpm debug", rel: "pnpm-debug.log", tool: "pnpm"},
		{name: "lerna debug", rel: "packages/lerna-debug.log", tool: "lerna"},
		{name: "jvm crash", rel: "hs_err_pid42.log", tool: "jvm"},
		{name: "jvm replay", rel: "replay_pid7.log", tool: "jvm"},
		{name: "rotated numeric", rel: "a.log.1", tool: "rotated-logs", conf: ConfidenceMedium},
		{name: "rotated gz", rel: "var/app.log.gz", tool: "rotated-logs", conf: ConfidenceMedium},
		{name: "plain log is not clutter", rel: "logs/app.log"},
		{name: "plain log at root", rel: "app.log"},
		{name: "log dir named like rotated", rel: "a.log.1", isDir: true},
		{name: "pytest cache", rel: ".pytest_cache", isDir: true, tool: "pytest", conf: ConfidenceHigh},
		{name: "pytest cache file", rel: ".pytest_cache"},
		{name: "mypy cache nested", rel: "svc/.mypy_cache", isDir: true, tool: "mypy"},
		{name: "ruff cache", rel: ".ruff_cache", isDir: true, tool: "ruff"},
		{name: "hypothesis", rel: ".hypothesis", isDir: true, tool: "hypothesis"},
		{name: "nox", rel: ".nox", isDir: true, tool: "nox"},
		{name: "eslintcache file", rel: ".eslintcache", tool: "eslint"},
		{name: "eslintcache as directory", rel: ".eslintcache", isDir: true},
		{name: "stylelintcache file", rel: "web/.stylelintcache", tool: "stylelint"},
		{name: "jest cache", rel: ".jest-cache", isDir: true, tool: "jest"},
		{name: "jest default cache is not matched", rel: "jest_abc123", isDir: true},
		{name: "sass cache", rel: ".sass-cache", isDir: true, tool: "sass"},
		{name: "coverage dir is medium", rel: "coverage", isDir: true, tool: "coverage", conf: ConfidenceMedium},
		{name: "coverage file is not a report", rel: "coverage"},
		{name: "coverage.py source", rel: "coverage.py"},
		{name: "nyc output", rel: ".nyc_output", isDir: true, tool: "coverage", conf: ConfidenceHigh},
		{name: "htmlcov", rel: "htmlcov", isDir: true, tool: "coverage"},
		{name: "coverage data file", rel: ".coverage", tool: "coverage"},
		{name: "coverage data as directory", rel: ".coverage", isDir: true},
		{name: "tox is build-artifacts", rel: ".tox", isDir: true},
		{name: "parcel cache is build-artifacts", rel: ".parcel-cache", isDir: true},
		{name: "vitest cache is unreachable", rel: "node_modules/.vite/vitest", isDir: true},
		{name: "ds store", rel: ".DS_Store", tool: "macos", conf: ConfidenceHigh},
		{name: "ds store nested", rel: "docs/.DS_Store", tool: "macos"},
		{name: "ds store directory", rel: ".DS_Store", isDir: true},
		{name: "appledouble is low", rel: "assets/._logo.png", tool: "macos", conf: ConfidenceLow},
		{name: "thumbs", rel: "img/Thumbs.db", tool: "windows", conf: ConfidenceHigh},
		{name: "ehthumbs", rel: "ehthumbs.db", tool: "windows"},
		{name: "desktop ini is low", rel: "desktop.ini", tool: "windows", conf: ConfidenceLow},
		{name: "vim swap", rel: ".file.swp", tool: "editors", conf: ConfidenceMedium},
		{name: "vim swap swo", rel: "src/.main.go.swo", tool: "editors"},
		{name: "backup tilde", rel: "notes.txt~", tool: "editors"},
		{name: "emacs lock", rel: ".#main.go", tool: "editors"},
		{name: "emacs autosave", rel: "#main.go#", tool: "editors"},
		{name: "core file", rel: "core", tool: "crash-dumps", conf: ConfidenceMedium},
		{name: "core nested", rel: "bin/core", tool: "crash-dumps"},
		{name: "core with pid", rel: "core.1234", tool: "crash-dumps"},
		{name: "core directory", rel: "core", isDir: true},
		{name: "core python module", rel: "core.py"},
		{name: "corefile text", rel: "corefile.txt"},
		{name: "score", rel: "score"},
		{name: "core suffix word", rel: "core.txt"},
		{name: "dmp", rel: "crash.dmp", tool: "crash-dumps"},
		{name: "stackdump", rel: "bash.exe.stackdump", tool: "crash-dumps"},
		{name: "ips is never blanket", rel: "report.ips"},
		{name: "npmrc is config", rel: ".npmrc"},
		{name: "yarnrc yml is config", rel: ".yarnrc.yml"},
		{name: "pytest ini is config", rel: "pytest.ini"},
		{name: "coveragerc is config", rel: ".coveragerc"},
		{name: "eslintrc is config", rel: ".eslintrc.json"},
		{name: "jest config is config", rel: "jest.config.js"},
		{name: "editorconfig is config", rel: ".editorconfig"},
		{name: "gitignore is config", rel: ".gitignore"},
		{name: "gitignore in nested dir", rel: "a/b/.gitignore"},
		{name: "case folds on darwin", goos: "darwin", rel: "Docs/.ds_store", tool: "macos"},
		{name: "case folds on windows", goos: "windows", rel: "IMG/THUMBS.DB", tool: "windows"},
		{name: "case sensitive on linux", goos: "linux", rel: "thumbs.db"},
		{name: "config stays protected on windows", goos: "windows", rel: ".NPMRC"},
	}
	matchers := map[string]*ProjectMatcher{}
	for _, tt := range tests {
		goos := tt.goos
		if goos == "" {
			goos = "linux"
		}
		m, ok := matchers[goos]
		if !ok {
			m = devCatalog(t, goos).ProjectMatcher()
			matchers[goos] = m
		}
		t.Run(tt.name, func(t *testing.T) {
			got, ok := m.Match(tt.rel, tt.isDir)
			if tt.tool == "" {
				if ok {
					t.Fatalf("Match(%q, dir=%v) = %s, want no match", tt.rel, tt.isDir, got.ToolID)
				}
				return
			}
			if !ok || got.ToolID != tt.tool {
				t.Fatalf("Match(%q, dir=%v) = %q, %v; want tool %q", tt.rel, tt.isDir, got.ToolID, ok, tt.tool)
			}
			if tt.conf != "" && got.Entry.Confidence != tt.conf {
				t.Errorf("confidence = %s, want %s", got.Entry.Confidence, tt.conf)
			}
		})
	}
}

// TestDevToolsProtectNeverMatches proves that config files and everything
// below a protected path stay out, whatever entry would otherwise match.
func TestDevToolsProtectNeverMatches(t *testing.T) {
	m := devCatalog(t, "linux").ProjectMatcher()
	for _, rel := range []string{
		".npmrc", ".yarnrc", ".yarnrc.yml", "pytest.ini", ".coveragerc", ".eslintrc", ".eslintrc.js",
		"jest.config.ts", ".editorconfig", ".gitignore", "pkg/.npmrc",
	} {
		if !m.Protected(rel) {
			t.Errorf("%q must be protected", rel)
		}
		for _, isDir := range []bool{false, true} {
			if _, ok := m.Match(rel, isDir); ok {
				t.Errorf("Match(%q, dir=%v) matched a protected path", rel, isDir)
			}
		}
	}
	// A directory that is protected shields what it contains even when a
	// clutter pattern fits; use a protect-shaped name with a swap file.
	if _, ok := m.Match(".gitignore/x.swp", false); ok {
		t.Error("below a protected path must not match")
	}
}

// devMachine builds a throwaway home and LOCALAPPDATA below one root, with
// every user location of the dev catalog present, so only the per-OS
// expansion decides what is returned. Bases are compared relative to root.
func devMachine(t *testing.T) (env PathEnv, root string) {
	t.Helper()
	root = t.TempDir()
	home, local := filepath.Join(root, "home"), filepath.Join(root, "local")
	for _, d := range []string{
		".npm/_logs", ".npm/_cacache", ".yarn/berry/cache",
		".cache/pip", ".cache/pypoetry/cache", ".cache/pypoetry/artifacts", ".cache/uv", ".cache/yarn",
		".cache/go-build", ".local/share/pnpm/store",
		"Library/Caches/pip", "Library/Caches/pypoetry/cache", "Library/Caches/pypoetry/artifacts",
		"Library/Caches/Yarn", "Library/Caches/go-build", "Library/pnpm/store", "Library/Logs/DiagnosticReports",
	} {
		mkdir(t, filepath.Join(home, filepath.FromSlash(d)))
	}
	for _, d := range []string{
		"npm-cache/_logs", "npm-cache/_cacache", "pip/Cache", "pypoetry/Cache/cache", "pypoetry/Cache/artifacts",
		"uv/cache", "Yarn/Cache", "go-build", "pnpm/store",
	} {
		mkdir(t, filepath.Join(local, filepath.FromSlash(d)))
	}
	vars := map[string]string{"LOCALAPPDATA": local}
	return PathEnv{Home: home, Getenv: func(k string) string { return vars[k] }}, root
}

func mkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestDevToolsUserLocationsPerOS(t *testing.T) {
	env, root := devMachine(t)
	tests := []struct {
		goos string
		want []string // "tool:base relative to the root", slash separated
	}{
		{"linux", []string{
			"npm:home/.npm/_logs", "npm-cache:home/.npm/_cacache", "yarn-cache:home/.cache/yarn", "yarn-cache:home/.yarn/berry/cache",
			"pip:home/.cache/pip", "poetry:home/.cache/pypoetry/cache", "poetry:home/.cache/pypoetry/artifacts",
			"uv:home/.cache/uv", "go-build:home/.cache/go-build", "pnpm-store:home/.local/share/pnpm/store",
		}},
		{"darwin", []string{
			"npm:home/.npm/_logs", "npm-cache:home/.npm/_cacache", "yarn-cache:home/Library/Caches/Yarn", "yarn-cache:home/.yarn/berry/cache",
			"pip:home/Library/Caches/pip", "poetry:home/Library/Caches/pypoetry/cache", "poetry:home/Library/Caches/pypoetry/artifacts",
			"uv:home/.cache/uv", "go-build:home/Library/Caches/go-build", "pnpm-store:home/Library/pnpm/store",
			"crash-dumps:home/Library/Logs/DiagnosticReports",
		}},
		{"windows", []string{
			"npm:local/npm-cache/_logs", "npm-cache:local/npm-cache/_cacache", "yarn-cache:local/Yarn/Cache",
			"yarn-cache:home/.yarn/berry/cache", "pip:local/pip/Cache", "poetry:local/pypoetry/Cache/cache",
			"poetry:local/pypoetry/Cache/artifacts", "uv:local/uv/cache", "go-build:local/go-build", "pnpm-store:local/pnpm/store",
		}},
	}
	for _, tt := range tests {
		t.Run(tt.goos, func(t *testing.T) {
			e := env
			e.GOOS = tt.goos
			locs := devCatalog(t, tt.goos).userLocations(e, CategoryLogs, CategoryCache, CategoryCrash)
			got := bases(t, locs, root)
			// One location per pattern; the crash report patterns share a base.
			slices.Sort(got)
			got = slices.Compact(got)
			want := slices.Clone(tt.want)
			slices.Sort(want)
			if !slices.Equal(got, want) {
				t.Errorf("locations\n got %v\nwant %v", got, want)
			}
		})
	}
}

func TestDevToolsUserLocationMatching(t *testing.T) {
	env, root := devMachine(t)
	home, local := filepath.Join(root, "home"), filepath.Join(root, "local")
	tests := []struct {
		name  string
		goos  string
		tool  string
		abs   string
		isDir bool
		want  bool
	}{
		{"npm log", "linux", "npm", filepath.Join(home, ".npm", "_logs", "2026-01-01T00_00_00_000Z-debug-0.log"), false, true},
		{"npm non log file", "linux", "npm", filepath.Join(home, ".npm", "_logs", "notes.txt"), false, false},
		{"npm base itself", "linux", "npm", filepath.Join(home, ".npm", "_logs"), true, false},
		{"pip top dir", "linux", "pip", filepath.Join(home, ".cache", "pip", "http-v2"), true, true},
		{"pip nested below top is reported as top only", "linux", "pip", filepath.Join(home, ".cache", "pip", "http-v2", "a"), true, false},
		{"go build entry", "darwin", "go-build", filepath.Join(home, "Library", "Caches", "go-build", "3f"), true, true},
		{"crash report node", "darwin", "crash-dumps", filepath.Join(home, "Library", "Logs", "DiagnosticReports", "node-2026-01-01-101010.ips"), false, true},
		{"crash report java", "darwin", "crash-dumps", filepath.Join(home, "Library", "Logs", "DiagnosticReports", "java-2026-01-01-101010.ips"), false, true},
		{"crash report python", "darwin", "crash-dumps", filepath.Join(home, "Library", "Logs", "DiagnosticReports", "python3.12-2026-01-01-101010.ips"), false, true},
		{"crash report vscode helper", "darwin", "crash-dumps", filepath.Join(home, "Library", "Logs", "DiagnosticReports", "Code Helper (Renderer)-2026-01-01-101010.ips"), false, true},
		{"crash report other app", "darwin", "crash-dumps", filepath.Join(home, "Library", "Logs", "DiagnosticReports", "Safari-2026-01-01-101010.ips"), false, false},
		{"crash report google chrome is not go", "darwin", "crash-dumps", filepath.Join(home, "Library", "Logs", "DiagnosticReports", "Google Chrome-2026-01-01-101010.ips"), false, false},
		{"crash report nodemon lookalike", "darwin", "crash-dumps", filepath.Join(home, "Library", "Logs", "DiagnosticReports", "nodemon-2026-01-01-101010.ips"), false, false},
		{"windows pnpm store child", "windows", "pnpm-store", filepath.Join(local, "pnpm", "store", "v3"), true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := env
			e.GOOS = tt.goos
			locs := devCatalog(t, tt.goos).userLocations(e)
			got := false
			for _, l := range locs {
				if l.ToolID == tt.tool && l.Match(tt.abs, tt.isDir) {
					got = true
				}
			}
			if got != tt.want {
				t.Errorf("Match(%q, dir=%v) = %v, want %v", tt.abs, tt.isDir, got, tt.want)
			}
		})
	}
}

// TestDevToolsUserLocationsAbsentOffPlatform checks that per-OS entries do
// not leak: Library paths on Linux, LOCALAPPDATA paths anywhere but Windows.
func TestDevToolsUserLocationsAbsentOffPlatform(t *testing.T) {
	env, _ := devMachine(t)
	for _, goos := range []string{"linux", "darwin"} {
		e := env
		e.GOOS = goos
		for _, l := range devCatalog(t, goos).userLocations(e) {
			if strings.Contains(l.Base, "npm-cache") {
				t.Errorf("%s: windows location %s leaked", goos, l.Base)
			}
		}
	}
	e := env
	e.GOOS = "linux"
	for _, l := range devCatalog(t, "linux").userLocations(e) {
		if strings.Contains(l.Base, "Library") {
			t.Errorf("linux: darwin location %s leaked", l.Base)
		}
	}
}
