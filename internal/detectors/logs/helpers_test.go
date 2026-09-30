package logs

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/detect"
	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/gitx"
	"github.com/Tobias-Braun/brooom/internal/scope"
	"github.com/Tobias-Braun/brooom/internal/testutil"
)

// now is the fixed scan time. Real file mtimes are later than it and would
// count as age 0, so tests set every mtime explicitly.
var now = testutil.BaseTime.AddDate(0, 0, 100)

// daysAgo is the mtime of a file that is n days old at scan time.
func daysAgo(n int) time.Time { return now.AddDate(0, 0, -n) }

// sandbox points every location the code under test might consult at a
// temporary home, so no test can touch the real home, trash or config.
func sandbox(t *testing.T) string {
	t.Helper()
	home := testutil.ResolvedTempDir(t)
	t.Setenv("BROOOM_HOME", filepath.Join(home, ".brooom"))
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("APPDATA", filepath.Join(home, "AppData", "Roaming"))
	t.Setenv("LOCALAPPDATA", filepath.Join(home, "AppData", "Local"))
	return home
}

// newEnv builds a detector environment whose guard allows the given paths.
func newEnv(t *testing.T, cfg *config.Config, allowed ...string) *detect.Env {
	t.Helper()
	runner, err := gitx.NewExecRunner()
	if err != nil {
		t.Skip("git not installed:", err)
	}
	g, err := scope.NewGuard(allowed...)
	if err != nil {
		t.Fatal(err)
	}
	return &detect.Env{
		Config:   cfg,
		Git:      runner,
		Repos:    gitx.NewCache(runner),
		Guard:    g,
		Now:      now,
		CacheDir: filepath.Join(testutil.ResolvedTempDir(t), "cache"),
	}
}

// repoTarget is the target of a git repository in repo mode.
func repoTarget(dir string) scope.Target {
	return scope.Target{Kind: scope.TargetRepo, Path: dir, Scope: findings.Scope{Type: findings.ScopeRepo, Path: dir}}
}

// projectTarget is a non-git project folder found below a workspace root.
func projectTarget(root, dir string) scope.Target {
	return scope.Target{Kind: scope.TargetProject, Path: dir, Scope: findings.Scope{Type: findings.ScopeRoot, Path: root}}
}

// extraTool declares a custom project-scope tool with one high confidence
// entry per call; minAge is the entry's own min_age_days.
func extraTool(id string, minAge int, patterns ...string) config.CatalogTool {
	return config.CatalogTool{
		ID: id, Name: "Tool " + id,
		Entries: []config.CatalogEntry{{
			Scope: "project", Patterns: patterns, Kind: "any", Confidence: "high",
			MinAgeDays: &minAge, Description: "test artifact of " + id,
		}},
	}
}

// cfgWith returns the default configuration plus the extra tools.
func cfgWith(extras ...config.CatalogTool) *config.Config {
	cfg := config.Default()
	cfg.Detectors.Logs.Extra = extras
	return cfg
}

// cfgDefault is the default configuration.
func cfgDefault() *config.Config { return config.Default() }

// envWithForce is a bare environment for tests of pure logic.
func envWithForce(force bool) *detect.Env { return &detect.Env{Force: force} }

// put creates a file below dir whose mtime is the given number of days old.
func put(t *testing.T, dir, rel string, ageDays int) string {
	t.Helper()
	p := testutil.WriteFile(t, dir, rel, "data\n")
	testutil.SetMTime(t, p, daysAgo(ageDays))
	return p
}

// oldDirs backdates every directory below and including root so the newest
// file decides a directory's age, and an empty directory is old too. Call it
// after all files exist.
func oldDirs(t *testing.T, root string) {
	t.Helper()
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() != ".git" {
			testutil.SetMTime(t, p, daysAgo(400))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// scan runs the detector on one target and collects its findings.
func scan(t *testing.T, env *detect.Env, target scope.Target) ([]findings.Finding, error) {
	t.Helper()
	return scanCtx(context.Background(), env, target)
}

func scanCtx(ctx context.Context, env *detect.Env, target scope.Target) ([]findings.Finding, error) {
	var got []findings.Finding
	err := New().Detect(ctx, env, target, func(f findings.Finding) { got = append(got, f) })
	return got, err
}

// mustScan is scan that fails the test on error.
func mustScan(t *testing.T, env *detect.Env, target scope.Target) []findings.Finding {
	t.Helper()
	got, err := scan(t, env, target)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// relPaths returns the finding paths relative to base, slash separated and
// sorted.
func relPaths(t *testing.T, fs []findings.Finding, base string) []string {
	t.Helper()
	out := []string{}
	for _, f := range fs {
		rel, err := filepath.Rel(base, f.Path)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, filepath.ToSlash(rel))
	}
	slices.Sort(out)
	return out
}

// byRel finds the finding at rel below base.
func byRel(t *testing.T, fs []findings.Finding, base, rel string) findings.Finding {
	t.Helper()
	want := filepath.Join(base, filepath.FromSlash(rel))
	for _, f := range fs {
		if f.Path == want {
			return f
		}
	}
	t.Fatalf("no finding for %s in %v", rel, relPaths(t, fs, base))
	return findings.Finding{}
}

// snapshot records every path below root with its size and mtime, to prove a
// scan modified nothing.
func snapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		out[p] = fmt.Sprintf("%s|%s|%d", fi.ModTime(), fi.Mode(), fi.Size())
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// hasFlag reports whether the finding carries the flag.
func hasFlag(f findings.Finding, flag findings.RiskFlag) bool { return f.HasRisk(flag) }

// symlinkOrSkip creates a symlink or skips the test where the OS refuses.
func symlinkOrSkip(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("cannot create symlinks here: %v", err)
	}
}

// elfCore is the start of a little-endian 64-bit ELF file of type ET_CORE, all
// a core dump needs to pass content verification.
const elfCore = "\x7fELF\x02\x01\x01\x00\x00\x00\x00\x00\x00\x00\x00\x00\x04\x00\x3e\x00"

// minidump is the signature of a Windows minidump.
const minidump = "MDMP\x93\xa7\x00\x00"

// putDump writes a file with the given header and backdates it, so a crash
// dump passes the content check that a plain put file fails.
func putDump(t *testing.T, dir, rel, content string, ageDays int) string {
	t.Helper()
	p := testutil.WriteFile(t, dir, rel, content)
	testutil.SetMTime(t, p, daysAgo(ageDays))
	return p
}
