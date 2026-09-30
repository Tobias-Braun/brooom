package logs

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/testutil"
)

// TestRepresentativeMatches walks the catalog categories with matches and
// look-alikes that must stay untouched, including the kind rules: a directory
// named core is no crash dump, a file named coverage is no coverage output.
func TestRepresentativeMatches(t *testing.T) {
	sandbox(t)
	repo := testutil.NewRepo(t)
	matched := []string{
		"npm-debug.log.1",        // logs
		"pnpm-debug.log",         // logs
		"hs_err_pid42.log",       // logs (JVM crash log)
		"a/app.log.3",            // rotated logs
		".mypy_cache/x",          // cache dir
		".eslintcache",           // cache file
		"coverage/lcov.info",     // cache dir
		".coverage",              // cache file
		".DS_Store",              // os-junk
		"sub/Thumbs.db",          // os-junk
		"notes.txt.swp",          // editor swap
		"core.123",               // crash dump file
		"core",                   // crash dump file
		"crash.dmp",              // crash dump
		"sub/dir/._resource",     // os-junk (AppleDouble)
		".hypothesis/examples/e", // cache dir
	}
	for _, m := range matched {
		put(t, repo.Dir, m, 100)
	}
	// Look-alikes: wrong kind or plain names that no entry claims.
	put(t, repo.Dir, "core-dir/core/x.txt", 100)      // a directory called core
	put(t, repo.Dir, "e/.eslintcache/inner.txt", 100) // a directory called .eslintcache
	put(t, repo.Dir, "f/coverage", 100)               // a file called coverage
	put(t, repo.Dir, "server.log", 100)               // plain logs have no entry
	put(t, repo.Dir, "corefile.txt", 100)
	oldDirs(t, repo.Dir)

	env := newEnv(t, config.Default(), repo.Dir)
	got := mustScan(t, env, repoTarget(repo.Dir))
	paths := relPaths(t, got, repo.Dir)
	for _, rel := range []string{
		"npm-debug.log.1", ".DS_Store", "pnpm-debug.log", "hs_err_pid42.log", "a/app.log.3", ".mypy_cache", ".eslintcache",
		"coverage", ".coverage", "sub/Thumbs.db", "notes.txt.swp", "core.123", "core", "crash.dmp",
		"sub/dir/._resource", ".hypothesis",
	} {
		if !contains(paths, rel) {
			t.Errorf("%s not reported; got %v", rel, paths)
		}
	}
	for _, rel := range []string{"core-dir/core", "e/.eslintcache", "f/coverage", "server.log", "corefile.txt"} {
		if contains(paths, rel) {
			t.Errorf("%s must not be reported; got %v", rel, paths)
		}
	}
	if f := byRel(t, got, repo.Dir, "coverage"); f.Kind != findings.KindDir || f.Confidence != findings.ConfidenceMedium {
		t.Errorf("coverage finding = %+v", f)
	}
	if f := byRel(t, got, repo.Dir, "core"); f.Kind != findings.KindFile || f.Meta["category"] != "crash" {
		t.Errorf("core finding = %+v", f)
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// TestKindRules isolates the kind rules with one directory or file per
// scenario, so a name that is a file in one case and a directory in another
// cannot collide.
func TestKindRules(t *testing.T) {
	cases := []struct {
		name  string
		files []string
		want  []string
	}{
		{"directory named core is not a crash dump", []string{"core/x.txt"}, []string{}},
		{"file named core.123 is", []string{"core.123"}, []string{"core.123"}},
		{"directory named .DS_Store is not junk", []string{".DS_Store/x"}, []string{}},
		{"file named .DS_Store is", []string{".DS_Store"}, []string{".DS_Store"}},
		{"directory named .eslintcache is not a cache file", []string{".eslintcache/x"}, []string{}},
		{"file named coverage is not coverage output", []string{"coverage"}, []string{}},
		{"directory named coverage is", []string{"coverage/x"}, []string{"coverage"}},
		{"plain log files have no entry", []string{"server.log", "debug.log"}, []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sandbox(t)
			dir := testutil.ResolvedTempDir(t)
			for _, f := range tc.files {
				put(t, dir, f, 100)
			}
			oldDirs(t, dir)
			env := newEnv(t, config.Default(), dir)
			got := relPaths(t, mustScan(t, env, projectTarget(dir, dir)), dir)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("paths = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestCategoryToggles proves that each of the four categories can be turned
// off on its own and that unlisted categories stay enabled.
func TestCategoryToggles(t *testing.T) {
	files := []string{"npm-debug.log", ".pytest_cache/x", ".DS_Store", "core.5"}
	all := []string{".DS_Store", ".pytest_cache", "core.5", "npm-debug.log"}
	cases := []struct {
		name       string
		categories map[string]bool
		want       []string
	}{
		{"defaults", nil, all},
		{"os-junk off", map[string]bool{"os-junk": false}, []string{".pytest_cache", "core.5", "npm-debug.log"}},
		{"crash off", map[string]bool{"crash": false}, []string{".DS_Store", ".pytest_cache", "npm-debug.log"}},
		{"cache off", map[string]bool{"cache": false}, []string{".DS_Store", "core.5", "npm-debug.log"}},
		{"logs off", map[string]bool{"logs": false}, []string{".DS_Store", ".pytest_cache", "core.5"}},
		{"explicit true is the default", map[string]bool{"crash": true}, all},
		{"all off", map[string]bool{"logs": false, "cache": false, "os-junk": false, "crash": false}, []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sandbox(t)
			dir := testutil.ResolvedTempDir(t)
			for _, f := range files {
				put(t, dir, f, 100)
			}
			oldDirs(t, dir)
			cfg := config.Default()
			cfg.Detectors.Logs.Categories = tc.categories
			env := newEnv(t, cfg, dir)
			got := relPaths(t, mustScan(t, env, projectTarget(dir, dir)), dir)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("paths = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestAIAndBuildCategoriesAreNeverHandled: ai and build tools belong to other
// detectors, even when their patterns would match.
func TestAIAndBuildCategoriesAreNeverHandled(t *testing.T) {
	sandbox(t)
	dir := testutil.ResolvedTempDir(t)
	put(t, dir, ".aider.chat.history.md", 100)
	put(t, dir, ".claude/projects/x.jsonl", 100)
	put(t, dir, "__pycache__/x.pyc", 100)
	oldDirs(t, dir)
	env := newEnv(t, config.Default(), dir)
	if got := relPaths(t, mustScan(t, env, projectTarget(dir, dir)), dir); len(got) != 0 {
		t.Errorf("paths = %v, want none", got)
	}
}

// TestExtraDefaultsToLogsCategory: an extra without a category is a logs
// entry, so it is toggled by categories.logs.
func TestExtraDefaultsToLogsCategory(t *testing.T) {
	sandbox(t)
	dir := testutil.ResolvedTempDir(t)
	put(t, dir, "custom.trace", 100)
	oldDirs(t, dir)
	cfg := cfgWith(extraTool("tracer", 0, "custom.trace"))
	env := newEnv(t, cfg, dir)
	got := mustScan(t, env, projectTarget(dir, dir))
	if len(got) != 1 || got[0].Meta["category"] != "logs" {
		t.Fatalf("findings = %+v", got)
	}
	cfg.Detectors.Logs.Categories = map[string]bool{"logs": false}
	if got := mustScan(t, newEnv(t, cfg, dir), projectTarget(dir, dir)); len(got) != 0 {
		t.Errorf("logs off: %d findings, want 0", len(got))
	}
}

// TestConfidence covers the recent-log downgrade: only names ending in .log
// or .log.N that changed within recent_days lose a level; directories and
// other files keep the entry's confidence and get recently_modified only.
func TestConfidence(t *testing.T) {
	cases := []struct {
		name       string
		file       string
		age        int
		confidence string
		want       findings.Confidence
		recent     bool
	}{
		{"recent rotated log, high", "a.log.1", 1, "high", findings.ConfidenceMedium, true},
		{"recent .log, high", "x.log", 0, "high", findings.ConfidenceMedium, true},
		{"recent .LOG, high", "X.LOG", 1, "high", findings.ConfidenceMedium, true},
		{"recent log, medium stays medium", "a.log.2", 1, "medium", findings.ConfidenceMedium, true},
		{"recent log, low stays low", "a.log.2", 1, "low", findings.ConfidenceLow, true},
		{"old rotated log keeps high", "a.log.1", 10, "high", findings.ConfidenceHigh, false},
		{"recent non-log keeps high", "b.trace", 1, "high", findings.ConfidenceHigh, true},
		{"recent directory keeps high", "dir.log/x", 1, "high", findings.ConfidenceHigh, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sandbox(t)
			dir := testutil.ResolvedTempDir(t)
			put(t, dir, tc.file, tc.age)
			oldDirs(t, dir)
			// The directory's own mtime counts for a directory finding, so it
			// must be as young as its content in the directory case.
			tool := extraTool("probe", 0, "a.log.1", "a.log.2", "x.log", "X.LOG", "b.trace", "dir.log")
			tool.Entries[0].Confidence = tc.confidence
			env := newEnv(t, cfgWith(tool), dir)
			if tc.file == "dir.log/x" {
				testutil.SetMTime(t, filepath.Join(dir, "dir.log"), daysAgo(tc.age))
			}
			got := mustScan(t, env, projectTarget(dir, dir))
			if len(got) != 1 {
				t.Fatalf("findings = %d, want 1", len(got))
			}
			f := got[0]
			if f.Confidence != tc.want || hasFlag(f, findings.RiskRecentlyModified) != tc.recent {
				t.Errorf("confidence %q flags %v, want %q recent=%v", f.Confidence, f.RiskFlags, tc.want, tc.recent)
			}
		})
	}
}

func TestIsLogName(t *testing.T) {
	for name, want := range map[string]bool{
		"a.log": true, "A.LOG": true, "a.log.1": true, "a.log.12": true,
		"a.log.gz": false, "a.logx": false, "log": false, "a.log.": false, "a.txt": false,
	} {
		if got := isLogName(name); got != want {
			t.Errorf("isLogName(%q) = %v, want %v", name, got, want)
		}
	}
}

// TestFreshDirectoryMtimeAfterCachedSize: a directory written in place after
// a cached DirSize still reports its new newest mtime, so recently_modified
// reflects reality.
func TestFreshDirectoryMtimeAfterCachedSize(t *testing.T) {
	sandbox(t)
	dir := testutil.ResolvedTempDir(t)
	log := put(t, dir, ".pytest_cache/v/log.txt", 100)
	oldDirs(t, dir)
	env := newEnv(t, config.Default(), dir)

	first := mustScan(t, env, projectTarget(dir, dir))
	if len(first) != 1 || hasFlag(first[0], findings.RiskRecentlyModified) {
		t.Fatalf("first run: %+v", first)
	}
	// Written in place: the file's mtime moves, the directories' do not.
	testutil.SetMTime(t, log, daysAgo(0))
	// The directory is now younger than the entry's min age, so it drops out
	// instead of being reported with the stale, cached date.
	if got := mustScan(t, env, projectTarget(dir, dir)); len(got) != 0 {
		t.Fatalf("second run: %d findings, want 0 (fresh mtime)", len(got))
	}
	// With the age requirement lifted the finding shows the fresh date.
	cfg := config.Default()
	zero := 0
	cfg.Detectors.Logs.MinAgeDays = &zero
	cfg.Thresholds.MinAgeDays = 0
	tool := extraTool("probe", 0, ".pytest_cache")
	cfg.Detectors.Logs.Extra = []config.CatalogTool{tool}
	cfg.Detectors.Logs.Categories = map[string]bool{"cache": false}
	got := mustScan(t, newEnv(t, cfg, dir), projectTarget(dir, dir))
	if len(got) != 1 || !hasFlag(got[0], findings.RiskRecentlyModified) || got[0].AgeDays != 0 {
		t.Fatalf("third run: %+v", got)
	}
}
