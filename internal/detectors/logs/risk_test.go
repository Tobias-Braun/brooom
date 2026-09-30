package logs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/procs"
	"github.com/Tobias-Braun/brooom/internal/testutil"
)

// fakeOpenFiles replaces the open-file check for one test and restores it.
func fakeOpenFiles(t *testing.T, fn func(context.Context, []string) (map[string]bool, error)) {
	t.Helper()
	orig := openFiles
	openFiles = fn
	t.Cleanup(func() { openFiles = orig })
}

func evidenceCodes(f findings.Finding) []string {
	var out []string
	for _, e := range f.Evidence {
		out = append(out, e.Code)
	}
	return out
}

func hasEvidence(f findings.Finding, code string) bool {
	for _, e := range f.Evidence {
		if e.Code == code {
			return true
		}
	}
	return false
}

// TestOpenCheckOutcomes drives the batched open-file check through its
// results: a process seen, nothing seen, and an unavailable or incomplete
// check. Open always wins, unknown is noted and never treated as safe.
func TestOpenCheckOutcomes(t *testing.T) {
	cases := []struct {
		name        string
		result      func(open, other string) (map[string]bool, error)
		wantOpen    bool
		wantOther   bool // open_check_unavailable evidence on the other file
		wantOpenEvi bool // open_check_unavailable evidence on the open file
	}{
		{
			name: "all checked, one open",
			result: func(open, other string) (map[string]bool, error) {
				return map[string]bool{open: true, other: false}, nil
			},
			wantOpen: true,
		},
		{
			name: "incomplete result still flags what was seen",
			result: func(open, other string) (map[string]bool, error) {
				return map[string]bool{open: true, other: false}, fmt.Errorf("%w: partial", procs.ErrIncomplete)
			},
			wantOpen: true, wantOther: true,
		},
		{
			name: "incomplete result without a hit notes the doubt",
			result: func(open, other string) (map[string]bool, error) {
				return map[string]bool{open: false, other: false}, procs.ErrIncomplete
			},
			wantOther: true, wantOpenEvi: true,
		},
		{
			name:      "unavailable check notes the doubt everywhere",
			result:    func(open, other string) (map[string]bool, error) { return nil, procs.ErrUnavailable },
			wantOther: true, wantOpenEvi: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sandbox(t)
			repo := testutil.NewRepo(t)
			open := put(t, repo.Dir, "npm-debug.log", 100)
			other := put(t, repo.Dir, "yarn-error.log", 100)
			oldDirs(t, repo.Dir)
			fakeOpenFiles(t, func(_ context.Context, paths []string) (map[string]bool, error) {
				if len(paths) != 2 {
					t.Errorf("open check called with %d paths, want one batch of 2", len(paths))
				}
				return tc.result(open, other)
			})
			env := newEnv(t, cfgDefault(), repo.Dir)
			for _, force := range []bool{false, true} {
				env.Force = force
				got := mustScan(t, env, repoTarget(repo.Dir))
				fo, ff := byRel(t, got, repo.Dir, "npm-debug.log"), byRel(t, got, repo.Dir, "yarn-error.log")
				if hasFlag(fo, findings.RiskFileOpen) != tc.wantOpen || hasFlag(ff, findings.RiskFileOpen) {
					t.Errorf("force=%v: open flags = %v / %v", force, fo.RiskFlags, ff.RiskFlags)
				}
				if hasEvidence(ff, evOpenUnavailable) != tc.wantOther || hasEvidence(fo, evOpenUnavailable) != tc.wantOpenEvi {
					t.Errorf("force=%v: evidence = %v / %v", force, evidenceCodes(fo), evidenceCodes(ff))
				}
				wantAction := findings.ActionTrash
				if tc.wantOpen {
					wantAction = findings.ActionNone
				}
				if fo.SuggestedAction.Type != wantAction || ff.SuggestedAction.Type != findings.ActionTrash {
					t.Errorf("force=%v: actions = %+v / %+v", force, fo.SuggestedAction, ff.SuggestedAction)
				}
			}
		})
	}
}

// TestOpenFileNeverOverridable uses the real check: a file this process holds
// open stays at action none with and without --force, even when it is also
// tracked (which --force alone would lift).
func TestOpenFileNeverOverridable(t *testing.T) {
	sandbox(t)
	repo := testutil.NewRepo(t)
	p := repo.WriteFile("npm-debug.log", "committed\n")
	repo.CommitAll("track", testutil.BaseTime)
	testutil.SetMTime(t, p, daysAgo(100))
	oldDirs(t, repo.Dir)

	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if res, err := procs.OpenFiles(context.Background(), []string{p}); !res[p] {
		t.Skipf("open-file detection does not see this process's own descriptors here: %v", err)
	}

	for _, force := range []bool{false, true} {
		env := newEnv(t, cfgDefault(), repo.Dir)
		env.Force = force
		got := byRel(t, mustScan(t, env, repoTarget(repo.Dir)), repo.Dir, "npm-debug.log")
		if !hasFlag(got, findings.RiskFileOpen) || !hasFlag(got, findings.RiskTrackedFiles) {
			t.Fatalf("force=%v: flags = %v", force, got.RiskFlags)
		}
		if got.SuggestedAction.Type != findings.ActionNone || !strings.Contains(got.SuggestedAction.Reason, string(findings.RiskFileOpen)) {
			t.Errorf("force=%v: action = %+v", force, got.SuggestedAction)
		}
	}
}

func TestRecentlyModifiedFlag(t *testing.T) {
	sandbox(t)
	dir := testutil.ResolvedTempDir(t)
	put(t, dir, ".fresh", 1)
	put(t, dir, ".stale", 10)
	oldDirs(t, dir)
	cfg := cfgWith(extraTool("probe", 0, ".fresh", ".stale"))
	env := newEnv(t, cfg, dir)
	got := mustScan(t, env, projectTarget(dir, dir))
	if f := byRel(t, got, dir, ".fresh"); !hasFlag(f, findings.RiskRecentlyModified) || f.SuggestedAction.Type != findings.ActionTrash {
		t.Errorf("fresh: flags %v action %+v (recently_modified is informational)", f.RiskFlags, f.SuggestedAction)
	}
	if f := byRel(t, got, dir, ".stale"); hasFlag(f, findings.RiskRecentlyModified) {
		t.Errorf("stale: flags %v", f.RiskFlags)
	}
}

func TestSuggestedActionMatrix(t *testing.T) {
	cases := []struct {
		name   string
		flags  []findings.RiskFlag
		force  bool
		want   findings.ActionType
		reason string
	}{
		{"no flags", nil, false, findings.ActionTrash, "desc"},
		{"informational flags only", []findings.RiskFlag{findings.RiskGitignored, findings.RiskRecentlyModified}, false, findings.ActionTrash, "desc"},
		{"tracked without force", []findings.RiskFlag{findings.RiskTrackedFiles}, false, findings.ActionNone, "tracked_files"},
		{"tracked with force", []findings.RiskFlag{findings.RiskTrackedFiles}, true, findings.ActionTrash, "forced: tracked_files"},
		{"open without force", []findings.RiskFlag{findings.RiskFileOpen}, false, findings.ActionNone, "file_open_by_process"},
		{"open with force", []findings.RiskFlag{findings.RiskFileOpen}, true, findings.ActionNone, "cannot override"},
		{"tracked and open with force", []findings.RiskFlag{findings.RiskTrackedFiles, findings.RiskFileOpen}, true, findings.ActionNone, "file_open_by_process"},
		{"force without blocking flags", []findings.RiskFlag{findings.RiskGitignored}, true, findings.ActionTrash, "desc"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &run{env: envWithForce(tc.force)}
			it := &item{flags: tc.flags}
			it.cand.entry.Description = "desc"
			got := r.action(it)
			if got.Type != tc.want || !strings.Contains(got.Reason, tc.reason) || got.Command != "" {
				t.Errorf("action = %+v, want %s containing %q", got, tc.want, tc.reason)
			}
		})
	}
}

// TestGitFailureFlagsTracked proves the conservative path: when git cannot
// answer, the finding is blocked rather than silently suggested.
func TestGitFailureFlagsTracked(t *testing.T) {
	sandbox(t)
	repo := testutil.NewRepo(t)
	put(t, repo.Dir, "npm-debug.log", 100)
	oldDirs(t, repo.Dir)
	env := newEnv(t, cfgDefault(), repo.Dir)
	// Corrupt the repository so that ls-files fails.
	if err := os.WriteFile(filepath.Join(repo.Dir, ".git", "HEAD"), []byte("garbage\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := byRel(t, mustScan(t, env, repoTarget(repo.Dir)), repo.Dir, "npm-debug.log")
	if !hasFlag(got, findings.RiskTrackedFiles) || !hasEvidence(got, evTrackedFailed) || got.SuggestedAction.Type != findings.ActionNone {
		t.Errorf("finding = %+v", got)
	}
}
