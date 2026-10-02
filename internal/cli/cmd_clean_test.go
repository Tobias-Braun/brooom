package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/action"
	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/detect"
	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/scope"
	"github.com/Tobias-Braun/brooom/internal/testutil"
)

// scanReport runs `brooom scan --format json` and decodes the report, which
// is the fixture of the round trip tests.
func scanReport(t *testing.T) *findings.Report {
	t.Helper()
	code, out, errOut := brooom(t, "", "scan", "--format", "json")
	if code != ExitOK {
		t.Fatalf("scan: code %d, stderr %q", code, errOut)
	}
	rep, err := findings.ReadReport(strings.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

// writeReportFile stores findings as a report file and returns its path.
func writeReportFile(t *testing.T, fs ...findings.Finding) string {
	t.Helper()
	rep := findings.NewReport("test", testutil.BaseTime, nil, fs, nil)
	return writeRaw(t, mustJSON(t, rep))
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func writeRaw(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "findings.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// trashFinding builds what a scan reports for a directory to trash. The
// scope is whatever the (untrusted) file claims.
func trashFinding(repoDir, path string, flags ...findings.RiskFlag) findings.Finding {
	return findings.Finding{
		ID:              findings.NewID("build-artifacts", findings.KindDir, path, ""),
		Detector:        "build-artifacts",
		Scope:           findings.Scope{Type: findings.ScopeRepo, Path: repoDir},
		Path:            path,
		Kind:            findings.KindDir,
		SizeBytes:       1,
		Confidence:      findings.ConfidenceHigh,
		SuggestedAction: findings.SuggestedAction{Type: findings.ActionTrash},
		RiskFlags:       append([]findings.RiskFlag{}, flags...),
	}
}

// branchFinding builds a delete-branch finding for repoDir.
func branchFinding(repoDir, ref string) findings.Finding {
	return findings.Finding{
		ID:              findings.NewID("merged-branch", findings.KindBranch, repoDir, ref),
		Detector:        "merged-branch",
		Scope:           findings.Scope{Type: findings.ScopeRepo, Path: repoDir},
		Path:            repoDir,
		Kind:            findings.KindBranch,
		Ref:             ref,
		SuggestedAction: findings.SuggestedAction{Type: findings.ActionDeleteBranch},
	}
}

// junkDir creates a directory with a file in it and returns both paths.
func junkDir(t *testing.T, parent, name string) (dir, file string) {
	t.Helper()
	file = testutil.WriteFile(t, filepath.Join(parent, name), "data.txt", "x")
	return filepath.Dir(file), file
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// clean runs `brooom clean` with the quarantine strategy so nothing reaches
// a real trash.
func clean(t *testing.T, stdin string, args ...string) (int, string, string) {
	t.Helper()
	return brooom(t, stdin, append(append([]string{"clean"}, args...), quarantine...)...)
}

func TestCleanRoundTripFromScan(t *testing.T) {
	f := newCleanupFixture(t, nil)
	f.mergedAndSquashed()
	rep := scanReport(t)
	if rep.Totals.Actionable < 2 {
		t.Fatalf("fixture has %d actionable findings", rep.Totals.Actionable)
	}
	path := writeReportFile(t, rep.Findings...)
	requireDryRunChangesNothing(t, f, path)

	code, out, errOut := clean(t, "", "--from", path, "--yes")
	if code != ExitOK {
		t.Fatalf("apply: code %d, stderr %q, stdout %q", code, errOut, out)
	}
	if f.hasBranch("feat/merged") || f.hasBranch("feat/squash") {
		t.Errorf("branches remain: %v", f.branches())
	}
	ms := f.sessions()
	if len(ms) != 1 || !strings.Contains(ms[0].Command, "clean --from") {
		t.Fatalf("want one clean session, got %+v", ms)
	}
}

func requireDryRunChangesNothing(t *testing.T, f *cleanupFixture, path string) {
	t.Helper()
	code, out, errOut := clean(t, "", "--from", path, "--dry-run")
	if code != ExitOK {
		t.Fatalf("dry run: code %d, stderr %q", code, errOut)
	}
	if !strings.Contains(out, "dry run: nothing was changed; re-run without --dry-run to execute") {
		t.Errorf("dry run output:\n%s", out)
	}
	if !f.hasBranch("feat/merged") || !f.hasBranch("feat/squash") || len(f.sessions()) != 0 {
		t.Fatalf("dry run changed something: %v", f.branches())
	}
}

func TestCleanFromStdin(t *testing.T) {
	f := newCleanupFixture(t, nil)
	f.mergedAndSquashed()
	data := mustJSON(t, scanReport(t))
	code, out, errOut := clean(t, data, "--from", "-", "--yes")
	if code != ExitOK {
		t.Fatalf("code %d, stderr %q, stdout %q", code, errOut, out)
	}
	if f.hasBranch("feat/merged") {
		t.Errorf("stdin report was not applied: %v", f.branches())
	}
}

func TestCleanInputErrors(t *testing.T) {
	newCleanupFixture(t, nil)
	valid := mustJSON(t, findings.NewReport("t", testutil.BaseTime, nil, nil, nil))
	newer := strings.Replace(valid, `"schema_version": 1`, `"schema_version": 99`, 1)
	zero := strings.Replace(valid, `"schema_version": 1`, `"schema_version": 0`, 1)
	missing := `{"findings": []}`
	tests := []struct {
		name  string
		file  string
		stdin string
		want  []string
	}{
		{"missing file", filepath.Join(t.TempDir(), "nope.json"), "", []string{"nope.json"}},
		{"directory", t.TempDir(), "", []string{"findings"}},
		{"empty file", writeRaw(t, ""), "", []string{"is empty"}},
		{"invalid json", writeRaw(t, "{not json"), "", []string{"invalid JSON"}},
		{"trailing data", writeRaw(t, valid+valid), "", []string{"unexpected data"}},
		{"newer schema", writeRaw(t, newer), "", []string{"schema_version 99", "upgrade brooom"}},
		{"zero schema", writeRaw(t, zero), "", []string{"not a brooom findings file"}},
		{"missing schema", writeRaw(t, missing), "", []string{"not a brooom findings file"}},
		{"empty stdin", "-", "", []string{"stdin", "is empty"}},
		{"invalid stdin", "-", "[", []string{"stdin", "invalid JSON"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, _, errOut := clean(t, tt.stdin, "--from", tt.file)
			if code != ExitError {
				t.Fatalf("code %d, want %d (stderr %q)", code, ExitError, errOut)
			}
			for _, w := range tt.want {
				if !strings.Contains(errOut, w) {
					t.Errorf("stderr lacks %q: %q", w, errOut)
				}
			}
		})
	}
}

func TestCleanUnknownFieldsTolerated(t *testing.T) {
	newCleanupFixture(t, nil)
	rep := mustJSON(t, findings.NewReport("t", testutil.BaseTime, nil, nil, nil))
	rep = strings.Replace(rep, "{", `{"future_field": {"a": 1},`, 1)
	code, out, errOut := clean(t, "", "--from", writeRaw(t, rep))
	if code != ExitOK || !strings.Contains(out, "nothing to clean") {
		t.Fatalf("code %d, out %q, stderr %q", code, out, errOut)
	}
}

func TestCleanIDSelection(t *testing.T) {
	f := newCleanupFixture(t, nil)
	f.mergedAndSquashed()
	rep := scanReport(t)
	path := writeReportFile(t, rep.Findings...)
	var merged, squash string
	for _, fd := range rep.Findings {
		switch fd.Ref {
		case "feat/merged":
			merged = fd.ID
		case "feat/squash":
			squash = fd.ID
		}
	}
	if merged == "" || squash == "" {
		t.Fatalf("fixture findings missing: %+v", rep.Findings)
	}

	t.Run("unknown ids fail and execute nothing", func(t *testing.T) {
		code, out, errOut := clean(t, "", "--from", path, "--yes", "--id", merged, "--id", "deadbeef,cafe")
		if code != ExitError {
			t.Fatalf("code %d, want 1", code)
		}
		if !strings.Contains(errOut, "deadbeef, cafe") || strings.Contains(errOut, merged) {
			t.Errorf("stderr should list exactly the unknown IDs: %q", errOut)
		}
		if strings.Contains(out, "applied") || !f.hasBranch("feat/merged") || len(f.sessions()) != 0 {
			t.Errorf("something was executed: %v", f.branches())
		}
	})
	t.Run("comma separated and repeated ids select", func(t *testing.T) {
		code, out, errOut := clean(t, "", "--from", path, "--yes", "--id", merged)
		if code != ExitOK {
			t.Fatalf("code %d, stderr %q, stdout %q", code, errOut, out)
		}
		if f.hasBranch("feat/merged") || !f.hasBranch("feat/squash") {
			t.Errorf("only feat/merged should be gone: %v", f.branches())
		}
	})
}

func TestCleanDuplicateIDsRunOnce(t *testing.T) {
	f := newCleanupFixture(t, nil)
	f.mergedAndSquashed()
	rep := scanReport(t)
	doubled := append(append([]findings.Finding{}, rep.Findings...), rep.Findings...)
	code, out, errOut := clean(t, "", "--from", writeReportFile(t, doubled...), "--yes")
	if code != ExitOK {
		t.Fatalf("code %d, stderr %q, stdout %q", code, errOut, out)
	}
	ms := f.sessions()
	if len(ms) != 1 || len(ms[0].Entries) != 2 {
		t.Fatalf("duplicates must be planned once: %+v", ms)
	}
}

func TestCleanRefusesOutsideScope(t *testing.T) {
	f := newCleanupFixture(t, nil)
	sibling := testutil.ResolvedTempDir(t)
	_, siblingFile := junkDir(t, sibling, "outside")
	otherRepo := testutil.NewRepo(t)
	otherRepo.Branch("victim")
	insideDir, insideFile := junkDir(t, f.repo.Dir, "junk")
	traversal := filepath.Join(f.repo.Dir, "..", filepath.Base(sibling), "outside")

	// The traversal spelling would get the same ID as the plain path.
	viaDots := trashFinding(f.repo.Dir, traversal)
	viaDots.ID = "via-dotdot"
	fs := []findings.Finding{
		trashFinding(f.repo.Dir, insideDir),
		trashFinding(f.repo.Dir, filepath.Dir(siblingFile)),
		viaDots,
		trashFinding(f.repo.Dir, "relative/path"),
		branchFinding(otherRepo.Dir, "victim"),
	}
	code, out, errOut := clean(t, "", "--from", writeReportFile(t, fs...), "--yes")
	if code != ExitError {
		t.Fatalf("code %d, want 1; stdout %q stderr %q", code, out, errOut)
	}
	if !strings.Contains(out, "refused findings (4)") {
		t.Errorf("refused heading missing:\n%s", out)
	}
	for _, id := range []string{fs[1].ID, fs[2].ID, fs[3].ID, fs[4].ID} {
		if !strings.Contains(out, id) {
			t.Errorf("refused list lacks %s:\n%s", id, out)
		}
	}
	if !exists(siblingFile) {
		t.Error("file outside the scope was touched")
	}
	if !strings.Contains(errOut, "4 finding(s) refused") {
		t.Errorf("stderr lacks the refusal count: %q", errOut)
	}
	// The valid finding of the same file is still executed.
	if exists(insideFile) {
		t.Error("the accepted finding was not executed")
	}
	if got := otherRepo.Git("branch", "--list", "victim"); !strings.Contains(got, "victim") {
		t.Error("branch of another repository was deleted")
	}
}

func TestCleanIDExcludesRefusedFindings(t *testing.T) {
	f := newCleanupFixture(t, nil)
	sibling := testutil.ResolvedTempDir(t)
	outside, _ := junkDir(t, sibling, "outside")
	inside, insideFile := junkDir(t, f.repo.Dir, "junk")
	good, bad := trashFinding(f.repo.Dir, inside), trashFinding(f.repo.Dir, outside)
	code, out, errOut := clean(t, "", "--from", writeReportFile(t, good, bad), "--yes", "--id", good.ID)
	if code != ExitOK {
		t.Fatalf("code %d, stdout %q, stderr %q", code, out, errOut)
	}
	if exists(insideFile) || strings.Contains(out, "refused") {
		t.Errorf("selected finding not applied or refusal reported:\n%s", out)
	}
}

func TestCleanRefusesSymlinkSwappedAfterScan(t *testing.T) {
	f := newCleanupFixture(t, nil)
	victim := testutil.ResolvedTempDir(t)
	victimFile := testutil.WriteFile(t, victim, "precious.txt", "keep")
	dir, _ := junkDir(t, f.repo.Dir, "cache")
	fd := trashFinding(f.repo.Dir, dir)
	path := writeReportFile(t, fd)

	// The attacker swaps the reviewed directory for a link to the outside.
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, dir); err != nil {
		t.Skipf("cannot create symlinks here: %v", err)
	}
	code, out, errOut := clean(t, "", "--from", path, "--yes")
	if code != ExitError || !strings.Contains(out, "symbolic link") {
		t.Fatalf("code %d, want refusal\nstdout %q\nstderr %q", code, out, errOut)
	}
	if !exists(victimFile) || !exists(dir) {
		t.Error("the symlink or its target was touched")
	}
}

func TestCleanRefusesParentSymlinkToOutside(t *testing.T) {
	f := newCleanupFixture(t, nil)
	victim := testutil.ResolvedTempDir(t)
	victimFile := testutil.WriteFile(t, filepath.Join(victim, "sub"), "precious.txt", "keep")
	link := filepath.Join(f.repo.Dir, "linked")
	if err := os.Symlink(victim, link); err != nil {
		t.Skipf("cannot create symlinks here: %v", err)
	}
	fd := trashFinding(f.repo.Dir, filepath.Join(link, "sub"))
	code, _, _ := clean(t, "", "--from", writeReportFile(t, fd), "--yes")
	if code != ExitError || !exists(victimFile) {
		t.Fatalf("code %d, target exists %v", code, exists(victimFile))
	}
}

func TestCleanPathCase(t *testing.T) {
	f := newCleanupFixture(t, nil)
	dir, file := junkDir(t, f.repo.Dir, "Build")
	upper := filepath.Join(f.repo.Dir, "BUILD")
	if !exists(upper) {
		t.Skip("case-sensitive file system")
	}
	code, out, errOut := clean(t, "", "--from", writeReportFile(t, trashFinding(f.repo.Dir, upper)), "--yes")
	if code != ExitOK {
		t.Fatalf("code %d, stdout %q, stderr %q", code, out, errOut)
	}
	if exists(file) || exists(dir) {
		t.Error("differently cased path was not trashed")
	}
}

func TestCleanRefusesUnsafeBranchNames(t *testing.T) {
	f := newCleanupFixture(t, nil)
	f.feature("safe")
	tests := []struct{ name, ref string }{
		{"leading dash", "-D"},
		{"long option", "--force"},
		{"newline", "a\nb"},
		{"escape", "a\x1b[31mb"},
		{"del", "a\x7fb"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, out, _ := clean(t, "", "--from", writeReportFile(t, branchFinding(f.repo.Dir, tt.ref)), "--yes")
			if code != ExitError || !strings.Contains(out, "refused findings (1)") {
				t.Fatalf("code %d\n%s", code, out)
			}
		})
	}
	if !f.hasBranch("safe") {
		t.Error("branch vanished")
	}
}

func TestCleanRefusesGitFindingOfForeignRepo(t *testing.T) {
	f := newCleanupFixture(t, nil)
	wt := branchFinding(f.repo.Dir, "x")
	wt.Kind, wt.SuggestedAction.Type = findings.KindWorktree, findings.ActionRemoveWorktree
	wt.Path = filepath.Join(f.repo.Dir, ".worktrees", "w")
	wt.Meta = map[string]string{"repo": testutil.ResolvedTempDir(t)}
	noRepo := wt
	noRepo.ID, noRepo.Meta = "no-repo", nil
	code, out, _ := clean(t, "", "--from", writeReportFile(t, wt, noRepo))
	if code != ExitError || !strings.Contains(out, "refused findings (2)") {
		t.Fatalf("code %d\n%s", code, out)
	}
}

func TestCleanTamperedRiskFlagsDoNotBypassPlan(t *testing.T) {
	f := newCleanupFixture(t, nil)
	f.mergedAndSquashed()
	rep := scanReport(t)
	for i := range rep.Findings {
		rep.Findings[i].RiskFlags = []findings.RiskFlag{}
	}
	// The branch gained a commit after the scan; the flag-free finding must
	// still be skipped by the action.
	f.repo.Git("checkout", "-q", "feat/merged")
	f.repo.Commit("late.txt", "late", "late work", f.at())
	f.repo.Checkout("main")

	code, out, errOut := clean(t, "", "--from", writeReportFile(t, rep.Findings...), "--yes")
	if code != ExitOK {
		t.Fatalf("code %d, stdout %q, stderr %q", code, out, errOut)
	}
	if !f.hasBranch("feat/merged") {
		t.Error("branch with new commits was deleted")
	}
}

func TestCleanOpenFileIsNotBypassed(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("open-file detection is only reliable via /proc in tests")
	}
	f := newCleanupFixture(t, nil)
	dir, file := junkDir(t, f.repo.Dir, "logs")
	h, err := os.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	code, out, errOut := clean(t, "", "--from", writeReportFile(t, trashFinding(f.repo.Dir, dir)), "--yes")
	if code != ExitOK {
		t.Fatalf("code %d, stdout %q, stderr %q", code, out, errOut)
	}
	if !exists(file) {
		t.Error("directory with an open file was trashed")
	}
}

func TestCleanExitCodes(t *testing.T) {
	f := newCleanupFixture(t, nil)
	dir, _ := junkDir(t, f.repo.Dir, "junk")
	path := writeReportFile(t, trashFinding(f.repo.Dir, dir))
	tests := []struct {
		name string
		args []string
		want int
	}{
		{"dry run", []string{"--from", path, "--dry-run"}, ExitOK},
		{"missing --from", nil, ExitUsage},
		{"acting needs confirmation", []string{"--from", path}, ExitUsage},
		{"bad trash strategy", []string{"--from", path, "--trash-strategy", "nope"}, ExitUsage},
		{"missing path", []string{"--from", path, "--path", filepath.Join(f.repo.Dir, "nope"), "--dry-run"}, ExitUsage},
		{"unknown flag", []string{"--from", path, "--bogus"}, ExitUsage},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, _, errOut := brooom(t, "", append([]string{"clean"}, tt.args...)...)
			if code != tt.want {
				t.Errorf("code %d, want %d (stderr %q)", code, tt.want, errOut)
			}
		})
	}
	if !exists(dir) {
		t.Error("a failing invocation removed the directory")
	}
}

func TestCleanOutsideRepoNeedsPath(t *testing.T) {
	isolate(t)
	t.Chdir(testutil.ResolvedTempDir(t))
	code, _, errOut := clean(t, "", "--from", writeRaw(t, mustJSON(t, findings.NewReport("t", testutil.BaseTime, nil, nil, nil))))
	if code != ExitUsage || !strings.Contains(errOut, scope.ErrNotInRepo.Error()) || !strings.Contains(errOut, "pass a folder") {
		t.Fatalf("code %d, stderr %q", code, errOut)
	}
}

func TestCleanScopeComesFromPath(t *testing.T) {
	needGit(t)
	isolate(t)
	// Real repositories: the trash action asks git which files are tracked.
	repoA, repoB := testutil.NewRepo(t).Dir, testutil.NewRepo(t).Dir
	t.Chdir(testutil.ResolvedTempDir(t))
	dirA, fileA := junkDir(t, repoA, "junk")
	dirB, fileB := junkDir(t, repoB, "junk")
	path := writeReportFile(t, trashFinding(repoA, dirA), trashFinding(repoB, dirB))

	// --path decides the scope: the finding in the other repository is
	// refused, whatever the file says.
	code, out, _ := clean(t, "", "--from", path, "--path", repoA, "--yes")
	if code != ExitError || !strings.Contains(out, "refused findings (1)") {
		t.Fatalf("narrowed run: code %d\n%s", code, out)
	}
	if exists(fileA) || !exists(fileB) {
		t.Errorf("narrowed run: a gone=%v b gone=%v", !exists(fileA), !exists(fileB))
	}
}

// userSource registers a fake TargetSource for `clean --user` returning base.
func userSource(t *testing.T, base string) {
	t.Helper()
	d := &sourcedDetector{
		fakeDetector: newFake(detect.CategoryAI, nil),
		extra: func(context.Context, *config.Config) ([]scope.Target, error) {
			return []scope.Target{{Kind: scope.TargetUser, Path: base, Scope: findings.Scope{Type: findings.ScopeUser, Path: base}, Tool: "fake"}}, nil
		},
	}
	old := cleanTargetSources
	cleanTargetSources = func() []detect.Detector { return []detect.Detector{d} }
	t.Cleanup(func() { cleanTargetSources = old })
}

func TestCleanUserScopeFindings(t *testing.T) {
	f := newCleanupFixture(t, nil)
	base := testutil.ResolvedTempDir(t)
	userSource(t, base)
	cache, cacheFile := junkDir(t, base, "cache")
	userFinding := trashFinding(base, cache)
	userFinding.Scope = findings.Scope{Type: findings.ScopeUser, Path: base}
	repoDir, repoFile := junkDir(t, f.repo.Dir, "junk")
	// Claims the user scope but lives in the repository, outside the base.
	lying := trashFinding(base, repoDir)
	lying.Scope = findings.Scope{Type: findings.ScopeUser, Path: base}

	t.Run("the removed --user flag", func(t *testing.T) {
		code, _, errOut := clean(t, "", "--from", writeReportFile(t, userFinding), "--user", "--dry-run")
		if code != ExitUsage || !strings.Contains(errOut, "--user") {
			t.Fatalf("code %d, stderr %q", code, errOut)
		}
	})
	t.Run("accepted in a location of the repository", func(t *testing.T) {
		code, out, errOut := clean(t, "", "--from", writeReportFile(t, userFinding), "--dry-run")
		if code != ExitOK || !strings.Contains(out, "cache") || !strings.Contains(out, "dry run") {
			t.Fatalf("code %d\nstdout %q\nstderr %q", code, out, errOut)
		}
		if !exists(cacheFile) {
			t.Error("dry run touched the file")
		}
	})
	t.Run("outside the user base is still refused", func(t *testing.T) {
		code, out, _ := clean(t, "", "--from", writeReportFile(t, lying), "--yes")
		if code != ExitError || !strings.Contains(out, "refused findings (1)") || !exists(repoFile) {
			t.Fatalf("code %d, file exists %v\n%s", code, exists(repoFile), out)
		}
	})
	t.Run("repo finding claiming user scope without base match", func(t *testing.T) {
		other := testutil.ResolvedTempDir(t)
		fd := trashFinding(base, other)
		fd.Scope.Type = findings.ScopeUser
		code, _, _ := clean(t, "", "--from", writeReportFile(t, fd), "--yes")
		if code != ExitError {
			t.Fatalf("code %d, want 1", code)
		}
	})
}

func TestCleanForceNeverUpgradesActionNone(t *testing.T) {
	f := newCleanupFixture(t, nil)
	blockedDir, blockedFile := junkDir(t, f.repo.Dir, "blocked")
	none := trashFinding(f.repo.Dir, blockedDir, findings.RiskWorktreeDirty)
	none.SuggestedAction = findings.SuggestedAction{Type: findings.ActionNone, Reason: "uncommitted changes"}

	code, out, errOut := clean(t, "", "--from", writeReportFile(t, none), "--force", "--yes")
	if code != ExitOK {
		t.Fatalf("code %d, stdout %q, stderr %q", code, out, errOut)
	}
	if !strings.Contains(out, "--force cannot add one") || !strings.Contains(out, "scan --force") {
		t.Errorf("re-scan hint missing:\n%s", out)
	}
	if !exists(blockedFile) {
		t.Error("an ActionNone finding was acted on")
	}
}

func TestCleanForceLiftsOverridableFlags(t *testing.T) {
	f := newCleanupFixture(t, nil)
	dir, file := junkDir(t, f.repo.Dir, "risky")
	path := writeReportFile(t, trashFinding(f.repo.Dir, dir, findings.RiskWorktreeDirty))

	code, out, errOut := clean(t, "", "--from", path, "--yes")
	if code != ExitOK || !exists(file) || !strings.Contains(out, "worktree_dirty") {
		t.Fatalf("without --force: code %d, file exists %v\nstdout %q\nstderr %q", code, exists(file), out, errOut)
	}
	code, out, errOut = clean(t, "", "--from", path, "--force", "--yes")
	if code != ExitOK || exists(file) {
		t.Fatalf("with --force: code %d, file exists %v\nstdout %q\nstderr %q", code, exists(file), out, errOut)
	}
}

// TestEveryKnownActionIsRegistered pins the assumption behind #238: every
// action type of the findings schema has an implementation, so `clean --from`
// needs no "not available in this build" skip for a known type.
func TestEveryKnownActionIsRegistered(t *testing.T) {
	for typ := range knownActions {
		if typ == findings.ActionNone {
			continue
		}
		if _, ok := action.Get(typ); !ok {
			t.Errorf("action %q is a known type but not registered", typ)
		}
	}
}

func TestCleanUnknownActionRefused(t *testing.T) {
	f := newCleanupFixture(t, nil)
	dir, file := junkDir(t, f.repo.Dir, "junk")
	bogus := trashFinding(f.repo.Dir, dir)
	bogus.SuggestedAction.Type = "rm-rf"
	code, out, _ := clean(t, "", "--from", writeReportFile(t, bogus), "--yes")
	if code != ExitError || !strings.Contains(out, `unknown action type "rm-rf"`) || !exists(file) {
		t.Fatalf("bogus action: code %d\n%s", code, out)
	}
}
