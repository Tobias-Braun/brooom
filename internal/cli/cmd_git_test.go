package cli

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/testutil"
)

// purgeFixture is a cleanup fixture (isolated home, repo with origin, cwd in
// the repo) with helpers for git maintenance state.
type purgeFixture struct {
	*cleanupFixture
}

func newPurgeFixture(t *testing.T, cfg map[string]any) *purgeFixture {
	t.Helper()
	return &purgeFixture{newCleanupFixture(t, cfg)}
}

// dangling writes an unreachable blob and returns its id.
func (f *purgeFixture) dangling(content string) string {
	f.t.Helper()
	f.repo.WriteFile("dangling.tmp", content)
	id := f.repo.Git("hash-object", "-w", "dangling.tmp")
	if err := os.Remove(filepath.Join(f.repo.Dir, "dangling.tmp")); err != nil {
		f.t.Fatal(err)
	}
	return id
}

func (f *purgeFixture) hasObject(id string) bool {
	f.t.Helper()
	return exec.Command("git", "-C", f.repo.Dir, "cat-file", "-e", id).Run() == nil
}

// workspaceConfig is a config with one root and no gh lookups.
func workspaceConfig(root string) map[string]any {
	cfg := rootsConfig(root)
	cfg["git"] = map[string]any{"use_gh": false}
	return cfg
}

// looseCommits creates many loose objects.
func (f *purgeFixture) looseCommits(n int) {
	f.t.Helper()
	for i := range n {
		f.repo.WriteFile(fmt.Sprintf("l%02d.txt", i), strings.Repeat("y", 50+i))
		f.repo.CommitAll(fmt.Sprintf("l%d", i), testutil.BaseTime.Add(time.Duration(i+1)*time.Minute))
	}
}

// gitSnapshot records path and size of every file below .git.
func (f *purgeFixture) gitSnapshot() map[string]int64 {
	f.t.Helper()
	out := map[string]int64{}
	root := filepath.Join(f.repo.Dir, ".git")
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		out[rel] = info.Size()
		return nil
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return out
}

func sameSnapshot(a, b map[string]int64) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			return false
		}
	}
	return true
}

// appliedActions returns the actions recorded by the only session.
func (f *purgeFixture) appliedActions() []findings.ActionType {
	f.t.Helper()
	ms := f.sessions()
	if len(ms) != 1 {
		f.t.Fatalf("%d sessions, want 1", len(ms))
	}
	var out []findings.ActionType
	for _, e := range ms[0].Entries {
		out = append(out, e.Action)
		if e.Restorable || len(e.Undo) != 0 || e.RecoveryHint == "" {
			f.t.Errorf("entry %+v must be non-restorable with a recovery hint", e)
		}
	}
	return out
}

func TestGitPurgeWithoutFlagsOnlyReports(t *testing.T) {
	f := newPurgeFixture(t, map[string]any{"detectors": map[string]any{"git-bloat": map[string]any{"loose_objects_threshold": 5}}})
	f.looseCommits(20)
	f.dangling("x")
	snap := f.gitSnapshot()

	code, out, errOut := brooom(t, "", "git", "purge")
	if code != ExitOK {
		t.Fatalf("code %d, stderr %q", code, errOut)
	}
	for _, want := range []string{"git-bloat", "--gc", "--reflog-expire <date>", "--prune <date>", "90.days.ago", "nothing was changed"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if !sameSnapshot(snap, f.gitSnapshot()) || len(f.sessions()) != 0 {
		t.Error("the report changed the repository or wrote a session")
	}

	code, out, _ = brooom(t, "", "git", "purge", "--format", "json")
	var report findings.Report
	if code != ExitOK || json.Unmarshal([]byte(out), &report) != nil || len(report.Findings) == 0 {
		t.Fatalf("json report: code %d, %q", code, out)
	}

	if code, _, errOut = brooom(t, "", "git", "purge", "--apply", "--yes"); code != ExitUsage || !strings.Contains(errOut, "--gc") {
		t.Errorf("--apply without an operation: code %d, %q", code, errOut)
	}
}

func TestGitPurgePruneDryRunThenApply(t *testing.T) {
	f := newPurgeFixture(t, nil)
	id := f.dangling("gone")
	snap := f.gitSnapshot()

	code, out, errOut := brooom(t, "", "git", "purge", "--prune", "now")
	if code != ExitOK {
		t.Fatalf("code %d, stderr %q", code, errOut)
	}
	for _, want := range []string{"NOT restorable", "git prune in", "permanently", "1 unreachable object", "brooom git purge --prune now --apply"} {
		if !strings.Contains(out, want) {
			t.Errorf("dry run lacks %q:\n%s", want, out)
		}
	}
	if !sameSnapshot(snap, f.gitSnapshot()) || !f.hasObject(id) || len(f.sessions()) != 0 {
		t.Fatal("the dry run changed something")
	}

	code, out, errOut = brooom(t, "", "git", "purge", "--prune", "now", "--apply", "--yes")
	if code != ExitOK {
		t.Fatalf("apply: code %d, stderr %q\n%s", code, errOut, out)
	}
	if f.hasObject(id) {
		t.Error("the dangling object survived")
	}
	if got := f.appliedActions(); len(got) != 1 || got[0] != findings.ActionGitPrune {
		t.Errorf("actions = %v, want only git-prune", got)
	}
	if !strings.Contains(f.sessions()[0].Entries[0].RecoveryHint, "deleted for good") {
		t.Errorf("hint = %q", f.sessions()[0].Entries[0].RecoveryHint)
	}
	if !strings.Contains(out, "reclaimed:") || !strings.Contains(out, "session:") {
		t.Errorf("summary lacks reclaimed bytes or the session id:\n%s", out)
	}
}

func TestGitPurgeEachFlagRunsOnlyItsAction(t *testing.T) {
	cfg := map[string]any{"detectors": map[string]any{"git-bloat": map[string]any{"loose_objects_threshold": 5, "prune_expire": "now"}}}
	tests := []struct {
		name string
		args []string
		want findings.ActionType
	}{
		{"reflog", []string{"--reflog-expire", "now"}, findings.ActionGitReflogExpire},
		{"prune", []string{"--prune", "now"}, findings.ActionGitPrune},
		{"gc", []string{"--gc"}, findings.ActionGitGC},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newPurgeFixture(t, cfg)
			f.looseCommits(20)
			f.dangling("x")
			code, out, errOut := brooom(t, "", append([]string{"git", "purge", "--apply", "--yes"}, tt.args...)...)
			if code != ExitOK {
				t.Fatalf("code %d, stderr %q\n%s", code, errOut, out)
			}
			if got := f.appliedActions(); len(got) != 1 || got[0] != tt.want {
				t.Errorf("actions = %v, want [%s]", got, tt.want)
			}
		})
	}
}

func TestGitPurgeGCOnlyTouchesReposWithFindings(t *testing.T) {
	f := newPurgeFixture(t, nil) // default thresholds: a fresh repo has no finding
	code, out, errOut := brooom(t, "", "git", "purge", "--gc", "--apply", "--yes")
	if code != ExitOK || !strings.Contains(out, "nothing to clean") {
		t.Fatalf("code %d, stderr %q\n%s", code, errOut, out)
	}
	if len(f.sessions()) != 0 {
		t.Error("gc ran on a repository without a bloat finding")
	}
}

func TestGitPurgeExplanations(t *testing.T) {
	f := newPurgeFixture(t, map[string]any{"detectors": map[string]any{"git-bloat": map[string]any{"loose_objects_threshold": 5}}})
	f.looseCommits(20)
	wording := []string{"gc.reflogExpire", "gc.reflogExpireUnreachable", "90 / 30", "unreachable objects", "worktree prune", "rerere gc"}

	_, help, _ := brooom(t, "", "git", "purge", "--help")
	for _, want := range append([]string{"90.days.ago", "2026-01-01", "recovery", "permanently", "rewrites packs"}, wording...) {
		if !strings.Contains(help, want) {
			t.Errorf("help lacks %q", want)
		}
	}

	code, out, errOut := brooom(t, "", "git", "purge", "--gc", "--reflog-expire", "90.days.ago", "--prune", "2.weeks.ago")
	if code != ExitOK {
		t.Fatalf("code %d, stderr %q", code, errOut)
	}
	for _, want := range append([]string{"NOT restorable", "Recovery points are lost", "permanently"}, wording...) {
		if !strings.Contains(out, want) {
			t.Errorf("dry run lacks %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "git gc in") {
		t.Errorf("no gc step planned:\n%s", out)
	}
}

func TestGitPurgeInvalidDatesAreUsageErrorsBeforeAnyChange(t *testing.T) {
	f := newPurgeFixture(t, nil)
	f.dangling("x")
	snap := f.gitSnapshot()
	for _, args := range [][]string{
		{"--prune", "garbage"}, {"--reflog-expire", "garbage"}, {"--prune=--all"}, {"--reflog-expire=--all"},
		{"--prune", ""}, {"--reflog-expire", ""}, {"--prune", "!!!"},
	} {
		code, _, errOut := brooom(t, "", append([]string{"git", "purge", "--apply", "--yes"}, args...)...)
		if code != ExitUsage {
			t.Errorf("%v: code %d, want %d (stderr %q)", args, code, ExitUsage, errOut)
		}
		if !sameSnapshot(snap, f.gitSnapshot()) || len(f.sessions()) != 0 {
			t.Fatalf("%v changed the repository", args)
		}
	}
}

func TestGitPurgeMachineFormatWithFlagsIsUsageError(t *testing.T) {
	newPurgeFixture(t, nil)
	code, _, errOut := brooom(t, "", "git", "purge", "--prune", "now", "--format", "json")
	if code != ExitUsage || !strings.Contains(errOut, "--format json") {
		t.Errorf("code %d, stderr %q", code, errOut)
	}
}

func TestGitPurgeSkipsRepoMidRebase(t *testing.T) {
	f := newPurgeFixture(t, nil)
	f.dangling("x")
	testutil.WriteFile(t, filepath.Join(f.repo.Dir, ".git"), "MERGE_HEAD", "abc\n")
	code, out, errOut := brooom(t, "", "git", "purge", "--prune", "now", "--apply", "--yes")
	if code != ExitOK || !strings.Contains(out, "in progress") {
		t.Fatalf("code %d, stderr %q\n%s", code, errOut, out)
	}
	if len(f.sessions()) != 0 {
		t.Error("a step ran in a repository with a merge in progress")
	}
}

func TestGitPurgeLinkedWorktreeIsOneOperationPerRepo(t *testing.T) {
	f := newPurgeFixture(t, nil)
	f.repo.Branch("side")
	wt := f.worktreeInRepo("wt", "side")
	f.dangling("x")

	// Run from inside the linked worktree: it resolves to the main worktree.
	t.Chdir(wt)
	code, out, errOut := brooom(t, "", "git", "purge", "--prune", "now")
	if code != ExitOK {
		t.Fatalf("code %d, stderr %q\n%s", code, errOut, out)
	}
	if n := strings.Count(out, "git prune in"); n != 1 {
		t.Errorf("%d prune steps, want 1:\n%s", n, out)
	}

	// The workspace scan sees the main and the linked worktree as targets.
	t.Chdir(f.repo.Dir)
	writeConfig(t, f.home, workspaceConfig(f.repo.Dir))
	code, out, errOut = brooom(t, "", "git", "purge", "--workspaces", "--prune", "now")
	if code != ExitOK {
		t.Fatalf("workspaces: code %d, stderr %q\n%s", code, errOut, out)
	}
	if n := strings.Count(out, "git prune in"); n != 1 {
		t.Errorf("workspaces: %d prune steps, want 1:\n%s", n, out)
	}
}
