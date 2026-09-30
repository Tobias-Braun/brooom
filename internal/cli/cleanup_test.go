package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/detect"
	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/session"
	"github.com/Tobias-Braun/brooom/internal/testutil"
)

// cleanupFixture is a repository with a bare origin, isolated Brooom home and
// HOME, and the working directory inside the repository.
type cleanupFixture struct {
	t    *testing.T
	repo *testutil.Repo
	home string
	n    int
}

// newCleanupFixture never uses gh (no network, no PR lookups) and never
// touches the real home or trash.
func newCleanupFixture(t *testing.T, cfg map[string]any) *cleanupFixture {
	t.Helper()
	needGit(t)
	home := isolate(t)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, ".gitconfig-none"))
	if cfg == nil {
		cfg = map[string]any{}
	}
	if _, ok := cfg["git"]; !ok {
		cfg["git"] = map[string]any{"use_gh": false}
	}
	writeConfig(t, home, cfg)
	repo := testutil.NewRepoWithRemote(t)
	t.Chdir(repo.Dir)
	return &cleanupFixture{t: t, repo: repo, home: home}
}

func (f *cleanupFixture) at() time.Time {
	f.n++
	return testutil.BaseTime.Add(time.Duration(f.n) * time.Hour)
}

// feature creates a branch with one commit off main and returns to main.
func (f *cleanupFixture) feature(name string) {
	f.t.Helper()
	f.repo.Git("checkout", "-q", "-b", name, "main")
	f.repo.Commit(strings.ReplaceAll(name, "/", "_")+".txt", name, "work on "+name, f.at())
	f.repo.Checkout("main")
}

// publish pushes main and refreshes origin/main, the base of merge detection.
func (f *cleanupFixture) publish() {
	f.t.Helper()
	f.repo.Push("main")
	f.repo.Fetch()
}

// mergeCommit merges a branch with a merge commit.
func (f *cleanupFixture) mergeCommit(name string) {
	f.t.Helper()
	f.repo.GitAt(f.at(), "merge", "-q", "--no-ff", "-m", "merge "+name, name)
}

// mergedAndSquashed creates one branch merged by a merge commit and one
// merged by squash and publishes main.
func (f *cleanupFixture) mergedAndSquashed() {
	f.feature("feat/merged")
	f.mergeCommit("feat/merged")
	f.feature("feat/squash")
	// The squash branch is on the remote: delete-branch never trusts the
	// patch-id heuristic alone for commits that exist nowhere else.
	f.repo.Git("push", "-q", "origin", "feat/squash")
	f.repo.SquashMerge("feat/squash", "squashed", f.at())
	f.publish()
}

// worktreeInRepo adds a linked worktree for an existing branch below the
// repository. Worktrees outside the scanned repository are outside the scope
// guard and deliberately skipped by the worktrees detector, so cleanup can
// only act on the ones that live inside it.
func (f *cleanupFixture) worktreeInRepo(name, branch string) string {
	f.t.Helper()
	p := filepath.Join(f.repo.Dir, ".worktrees", name)
	f.repo.Git("worktree", "add", "-q", p, branch)
	return p
}

func (f *cleanupFixture) branches() []string {
	f.t.Helper()
	var out []string
	for _, l := range strings.Split(f.repo.Git("branch", "--format=%(refname:short)"), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

func (f *cleanupFixture) hasBranch(name string) bool { return slices.Contains(f.branches(), name) }

func (f *cleanupFixture) sessions() []*session.Manifest {
	f.t.Helper()
	ms, problems, err := session.NewStore(filepath.Join(f.home, "sessions")).List()
	if err != nil && !os.IsNotExist(err) {
		f.t.Fatal(err)
	}
	if len(problems) > 0 {
		f.t.Fatalf("session problems: %v", problems)
	}
	return ms
}

// brooom runs the CLI through Main with a scripted stdin (never a TTY).
func brooom(t *testing.T, stdin string, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := Main(args, IO{In: strings.NewReader(stdin), Out: &out, Err: &errOut})
	return code, out.String(), errOut.String()
}

// quarantine is the trash strategy of every test that could trash files.
var quarantine = []string{"--trash-strategy", "quarantine"}

// mergedSweep sweeps the merged branches only; tests add --dry-run or --yes.
var mergedSweep = []string{"sweep", "after-agents", "-d", "merged-branch"}

func sweepArgs(extra ...string) []string { return append(slices.Clone(mergedSweep), extra...) }

func TestMergedSweepDryRunChangesNothing(t *testing.T) {
	f := newCleanupFixture(t, nil)
	f.mergedAndSquashed()
	code, out, errOut := brooom(t, "", sweepArgs("--dry-run")...)
	if code != ExitOK {
		t.Fatalf("code %d, stderr %q", code, errOut)
	}
	for _, want := range []string{"feat/merged", "feat/squash", "dry run: nothing was changed; re-run without --dry-run to execute"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if !f.hasBranch("feat/merged") || !f.hasBranch("feat/squash") {
		t.Errorf("dry run deleted branches: %v", f.branches())
	}
	if got := f.sessions(); len(got) != 0 {
		t.Errorf("dry run wrote %d session(s)", len(got))
	}
}

func TestSweepYesDeletesAndRecords(t *testing.T) {
	f := newCleanupFixture(t, nil)
	f.mergedAndSquashed()
	code, out, errOut := brooom(t, "", sweepArgs("--yes")...)
	if code != ExitOK {
		t.Fatalf("code %d, stderr %q, stdout %q", code, errOut, out)
	}
	if f.hasBranch("feat/merged") || f.hasBranch("feat/squash") {
		t.Fatalf("branches remain: %v", f.branches())
	}
	ms := f.sessions()
	if len(ms) != 1 {
		t.Fatalf("want 1 session, got %d", len(ms))
	}
	m := ms[0]
	requireRestorableEntries(t, m, 2)
	if !strings.Contains(out, "brooom undo "+m.ID) {
		t.Errorf("summary lacks the undo command for %s:\n%s", m.ID, out)
	}
	if want := "brooom sweep after-agents -d merged-branch --yes"; m.Command != want {
		t.Errorf("manifest command = %q, want %q", m.Command, want)
	}

	code, out, _ = brooom(t, "", sweepArgs("--yes")...)
	if code != ExitOK || !strings.Contains(out, "nothing to clean") {
		t.Errorf("second run: code %d, output %q", code, out)
	}
	if len(f.sessions()) != 1 {
		t.Errorf("second run created a session")
	}
}

// TestSweepAsksOnceInTheSameRun: without --yes and --dry-run sweep shows the
// plan, asks one question and acts on a yes; anything else changes nothing.
func TestSweepAsksOnceInTheSameRun(t *testing.T) {
	for _, tc := range []struct {
		answer  string
		deleted bool
	}{{"y\n", true}, {"n\n", false}, {"\n", false}, {"", false}} {
		t.Run(strconv.Quote(tc.answer), func(t *testing.T) {
			f := newCleanupFixture(t, nil)
			f.mergedAndSquashed()
			code, out, errOut := runApp(t, tc.answer, true, time.Time{}, mergedSweep...)
			if code != ExitOK {
				t.Fatalf("code %d, stderr %q", code, errOut)
			}
			if n := strings.Count(out, "[y/N]"); n != 1 {
				t.Errorf("%d questions, want 1:\n%s", n, out)
			}
			plan, question := strings.Index(out, "feat/merged"), strings.Index(out, "Proceed with 2 items")
			if plan < 0 || question < plan {
				t.Errorf("the plan must come before the question:\n%s", out)
			}
			if got := !f.hasBranch("feat/merged"); got != tc.deleted {
				t.Errorf("deleted = %v, want %v:\n%s", got, tc.deleted, out)
			}
		})
	}
}

// requireRestorableEntries checks that a manifest holds n applied entries
// that undo can restore.
func requireRestorableEntries(t *testing.T, m *session.Manifest, n int) {
	t.Helper()
	if len(m.Entries) != n {
		t.Fatalf("want %d entries, got %+v", n, m.Entries)
	}
	for _, e := range m.Entries {
		if e.Status != session.StatusApplied || !e.Restorable {
			t.Errorf("entry %s: status %s restorable %v", e.Ref, e.Status, e.Restorable)
		}
	}
}

func TestSweepWithoutConfirmationIsUsageError(t *testing.T) {
	f := newCleanupFixture(t, nil)
	f.mergedAndSquashed()
	code, _, errOut := brooom(t, "", mergedSweep...)
	if code != ExitUsage || !strings.Contains(errOut, "--dry-run") {
		t.Fatalf("code %d, want %d; stderr %q", code, ExitUsage, errOut)
	}
	if !f.hasBranch("feat/merged") || !f.hasBranch("feat/squash") {
		t.Errorf("branches changed: %v", f.branches())
	}
	if len(f.sessions()) != 0 {
		t.Errorf("a session was written")
	}
}

// TestSweepHasNoForce: sweep never acts on unmerged or dirty work, so it has
// no --force; clean --from is where an overridable risk flag can be lifted.
func TestSweepHasNoForce(t *testing.T) {
	newCleanupFixture(t, nil)
	code, _, errOut := brooom(t, "", "sweep", "--force", "--dry-run")
	if code != ExitUsage || !strings.Contains(errOut, "--force") {
		t.Errorf("code %d, stderr %q", code, errOut)
	}
}

// forceClean exports the findings of a detector with --force and acts on them
// with clean --from --force --yes.
func forceClean(t *testing.T, detector string) (int, string, string) {
	t.Helper()
	_, out, _ := brooom(t, "", "scan", "-d", detector, "--force", "--format", "json")
	return brooom(t, "", "clean", "--from", writeRaw(t, out), "--yes", "--force")
}

func TestStaleBranchesNeedForce(t *testing.T) {
	cfg := map[string]any{"detectors": map[string]any{"stale-branch": map[string]any{"enabled": true, "min_age_days": 1, "include_unpushed": true}}}
	f := newCleanupFixture(t, cfg)
	f.feature("feat/old")

	code, out, errOut := brooom(t, "", "sweep", "--yes")
	if code != ExitOK {
		t.Fatalf("code %d, stderr %q", code, errOut)
	}
	if !f.hasBranch("feat/old") {
		t.Fatalf("sweep deleted an unmerged branch:\n%s", out)
	}

	code, out, errOut = forceClean(t, config.DetectorStaleBranch)
	if code != ExitOK {
		t.Fatalf("code %d, stderr %q\n%s", code, errOut, out)
	}
	if f.hasBranch("feat/old") {
		t.Errorf("--force did not delete the unmerged branch:\n%s", out)
	}
}

func TestForceNeverDeletesCheckedOutBranch(t *testing.T) {
	cfg := map[string]any{"detectors": map[string]any{"stale-branch": map[string]any{"enabled": true, "min_age_days": 1, "include_unpushed": true}}}
	f := newCleanupFixture(t, cfg)
	f.feature("feat/current")
	f.repo.Checkout("feat/current")
	code, out, errOut := forceClean(t, config.DetectorStaleBranch)
	if code != ExitOK && code != ExitError {
		t.Fatalf("code %d, stderr %q\n%s", code, errOut, out)
	}
	if !f.hasBranch("feat/current") {
		t.Errorf("checked-out branch was deleted:\n%s", out)
	}
}

// worktreeSweep sweeps the worktrees only.
var worktreeSweep = []string{"sweep", "after-agents", "-d", "worktrees"}

func TestWorktreesCleanup(t *testing.T) {
	f := newCleanupFixture(t, nil)
	f.feature("feat/wt")
	merged := f.worktreeInRepo("merged", "feat/wt")
	f.mergeCommit("feat/wt")
	f.publish()

	f.feature("feat/gone")
	goneDir := f.worktreeInRepo("gone", "feat/gone")
	if err := os.RemoveAll(goneDir); err != nil {
		t.Fatal(err)
	}

	code, out, errOut := brooom(t, "", append(slices.Clone(worktreeSweep), "--dry-run")...)
	if code != ExitOK {
		t.Fatalf("code %d, stderr %q", code, errOut)
	}
	if !strings.Contains(out, "re-run without --dry-run") {
		t.Errorf("dry run lacks the hint:\n%s", out)
	}
	if _, err := os.Stat(merged); err != nil {
		t.Fatalf("dry run removed the worktree: %v", err)
	}

	code, out, errOut = brooom(t, "", append(append(slices.Clone(worktreeSweep), "--yes"), quarantine...)...)
	if code != ExitOK {
		t.Fatalf("apply: code %d, stderr %q\n%s", code, errOut, out)
	}
	if _, err := os.Stat(merged); !os.IsNotExist(err) {
		t.Errorf("merged worktree still exists (%v):\n%s", err, out)
	}
	if list := f.repo.Git("worktree", "list", "--porcelain"); strings.Contains(list, "gone") {
		t.Errorf("missing-directory worktree was not pruned:\n%s", list)
	}
}

// detachedWorktree adds a worktree detached at the tip of a fresh feature
// branch, the way an agent leaves it behind. With landed, the branch commit is
// then rebase-merged into main under a new id and the branch deleted, so the
// worktree HEAD is reachable from no ref but patch-equivalent to the base.
func (f *cleanupFixture) detachedWorktree(name string, landed bool) string {
	f.t.Helper()
	branch := "feat/" + name
	f.feature(branch)
	p := filepath.Join(f.repo.Dir, ".worktrees", name)
	f.repo.Git("worktree", "add", "-q", "--detach", p, branch)
	if landed {
		f.repo.RebaseMerge(branch, f.at())
	}
	f.repo.Git("branch", "-D", branch)
	return p
}

// TestAfterAgentsSweep covers issue #255 with the after-agents preset: right
// after a large agent run every worktree is seconds old, and the sweep must
// still remove the clean merged ones and the detached ones whose commits
// already landed under other ids, while dirty worktrees and detached ones with
// unique commits stay. undo brings the removed ones back.
func TestAfterAgentsSweep(t *testing.T) {
	f := newCleanupFixture(t, nil)
	removed := map[string]bool{}
	kept := map[string]bool{}
	for i := 0; i < 7; i++ {
		name := "clean" + strconv.Itoa(i)
		f.feature("feat/" + name)
		removed[f.worktreeInRepo(name, "feat/"+name)] = true
		f.mergeCommit("feat/" + name)
	}
	for i := 0; i < 5; i++ {
		name := "dirty" + strconv.Itoa(i)
		f.feature("feat/" + name)
		p := f.worktreeInRepo(name, "feat/"+name)
		f.mergeCommit("feat/" + name)
		testutil.WriteFile(t, p, "scratch.txt", "uncommitted\n")
		kept[p] = true
	}
	// An edit hidden from git status by skip-worktree is still uncommitted work.
	f.feature("feat/hidden")
	hidden := f.worktreeInRepo("hidden", "feat/hidden")
	f.mergeCommit("feat/hidden")
	f.repo.Git("-C", hidden, "update-index", "--skip-worktree", "feat_hidden.txt")
	testutil.WriteFile(t, hidden, "feat_hidden.txt", "local override\n")
	kept[hidden] = true
	for i := 0; i < 5; i++ {
		removed[f.detachedWorktree("rebased"+strconv.Itoa(i), true)] = true
	}
	for i := 0; i < 3; i++ {
		kept[f.detachedWorktree("unique"+strconv.Itoa(i), false)] = true
	}
	f.publish()

	code, out, errOut := brooom(t, "", append([]string{"sweep", "after-agents", "--yes"}, quarantine...)...)
	if code != ExitOK {
		t.Fatalf("apply: code %d, stderr %q\n%s", code, errOut, out)
	}
	for p := range removed {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s should be removed (%v)", filepath.Base(p), err)
		}
	}
	for p := range kept {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s must stay: %v", filepath.Base(p), err)
		}
	}

	code, out, errOut = brooom(t, "", "undo", "--yes")
	if code != ExitOK {
		t.Fatalf("undo: code %d, stderr %q\n%s", code, errOut, out)
	}
	list := f.repo.Git("worktree", "list", "--porcelain")
	for p := range removed {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s not restored: %v", filepath.Base(p), err)
		}
		if !listsWorktree(list, p) {
			t.Errorf("%s is not a registered worktree again", filepath.Base(p))
		}
	}
}

// listsWorktree reports whether the porcelain worktree list names path. git
// prints registered paths with forward slashes and long names, so the
// comparison uses the resolved, slash-normalised form (Windows uses
// backslashes and may hand out 8.3 short temp directory names).
func listsWorktree(list, path string) bool {
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		real = path
	}
	return strings.Contains(filepath.ToSlash(list), filepath.ToSlash(real))
}

// TestTidyLeavesGitAlone: the tidy preset only runs the logs detector, so
// merged worktrees and branches survive it.
func TestTidyLeavesGitAlone(t *testing.T) {
	f := newCleanupFixture(t, nil)
	f.feature("feat/merged")
	merged := f.worktreeInRepo("merged", "feat/merged")
	f.mergeCommit("feat/merged")
	f.publish()

	code, out, errOut := brooom(t, "", append([]string{"sweep", "tidy", "--yes"}, quarantine...)...)
	if code != ExitOK {
		t.Fatalf("tidy: code %d, stderr %q\n%s", code, errOut, out)
	}
	if _, err := os.Stat(merged); err != nil || !f.hasBranch("feat/merged") {
		t.Errorf("tidy touched git: %v, branches %v", err, f.branches())
	}
}

func TestDirtyWorktreeIsNeverRemovedBySweep(t *testing.T) {
	f := newCleanupFixture(t, nil)
	f.feature("feat/dirty")
	wt := f.worktreeInRepo("dirty", "feat/dirty")
	f.mergeCommit("feat/dirty")
	f.publish()
	testutil.WriteFile(t, wt, "scratch.txt", "uncommitted\n")

	code, out, errOut := brooom(t, "", append([]string{"sweep", "--yes"}, quarantine...)...)
	if code != ExitOK {
		t.Fatalf("code %d, stderr %q\n%s", code, errOut, out)
	}
	if _, err := os.Stat(filepath.Join(wt, "scratch.txt")); err != nil {
		t.Errorf("dirty worktree content lost: %v\n%s", err, out)
	}
}

func TestMachineFormats(t *testing.T) {
	f := newCleanupFixture(t, nil)
	f.mergedAndSquashed()

	// A machine format only reports, like --dry-run.
	code, out, errOut := brooom(t, "", sweepArgs("--format", "json")...)
	if code != ExitOK {
		t.Fatalf("code %d, stderr %q", code, errOut)
	}
	var report findings.Report
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("stdout is not a report: %v\n%s", err, out)
	}
	if len(report.Findings) != 2 {
		t.Errorf("want 2 findings, got %d", len(report.Findings))
	}
	if strings.Contains(out, "re-run") {
		t.Errorf("machine output contains plan text")
	}

	for _, format := range []string{"json", "ndjson", "plain"} {
		code, _, errOut = brooom(t, "", sweepArgs("--format", format, "--yes")...)
		if code != ExitUsage || !strings.Contains(errOut, "--format "+format+" only reports and cannot be combined with --yes") {
			t.Errorf("%s: code %d, stderr %q", format, code, errOut)
		}
	}
	if !f.hasBranch("feat/merged") {
		t.Errorf("a machine-format sweep changed something")
	}
}

// TestConfigFormatDoesNotBlockSweep reproduces #110: a machine format that
// only comes from output.format in the config must not make an acting run
// fail with a complaint about a flag the user never passed. Dry runs still
// honour it.
func TestConfigFormatDoesNotBlockSweep(t *testing.T) {
	for _, format := range []string{"json", "ndjson", "plain"} {
		t.Run(format, func(t *testing.T) {
			f := newCleanupFixture(t, map[string]any{"output": map[string]any{"format": format}})
			f.mergedAndSquashed()

			_, out, _ := brooom(t, "", sweepArgs("--dry-run")...)
			if strings.Contains(out, "dry run") {
				t.Errorf("a dry run ignored the configured %s format:\n%s", format, out)
			}
			code, out, errOut := brooom(t, "", sweepArgs("--yes")...)
			if code != ExitOK {
				t.Fatalf("code %d, stderr %q", code, errOut)
			}
			if !strings.Contains(out, "applied") && !strings.Contains(out, "removed") {
				t.Errorf("no human summary:\n%s", out)
			}
			if f.hasBranch("feat/merged") {
				t.Errorf("sweep did not run: %v", f.branches())
			}
		})
	}
}

// TestConfigFormatDoesNotBlockCleanAndPurge: the other acting commands follow
// the same rule; clean --from never looks at the format when acting.
func TestConfigFormatDoesNotBlockCleanAndPurge(t *testing.T) {
	f := newCleanupFixture(t, map[string]any{"output": map[string]any{"format": "json"}})
	dir, _ := junkDir(t, f.repo.Dir, "target")
	report := writeReportFile(t, trashFinding(f.repo.Dir, dir))
	code, _, errOut := brooom(t, "", "clean", "--from", report, "--yes", "--trash-strategy", "quarantine")
	if code != ExitOK || exists(dir) {
		t.Errorf("clean: code %d, stderr %q, dir exists %v", code, errOut, exists(dir))
	}
	code, _, errOut = brooom(t, "", "git", "purge", "--prune", "now", "--dry-run")
	if code != ExitOK {
		t.Errorf("git purge: code %d, stderr %q", code, errOut)
	}
}

func TestResolveActingFormat(t *testing.T) {
	tests := []struct {
		name, flag, cfg, want string
	}{
		{"config machine format falls back", "", "json", "table"},
		{"config human format is kept", "", "summary", "summary"},
		{"flag wins and is kept for the caller to refuse", "ndjson", "table", "ndjson"},
		{"default", "", "", "table"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveActingFormat(tt.flag, tt.cfg)
			if err != nil || got != tt.want {
				t.Errorf("got %q, %v; want %q", got, err, tt.want)
			}
		})
	}
	if _, err := resolveActingFormat("", "bogus"); err == nil {
		t.Error("an unknown config format was accepted")
	}
}

func TestDetectorOutsideThePreset(t *testing.T) {
	newCleanupFixture(t, nil)
	code, _, errOut := brooom(t, "", "sweep", "tidy", "--detector", "worktrees", "--dry-run")
	if code != ExitUsage || !strings.Contains(errOut, "not part of the tidy preset; the after-agents preset includes it") {
		t.Errorf("code %d, stderr %q", code, errOut)
	}
	code, _, errOut = brooom(t, "", "sweep", "--detector", "stale-branch", "--dry-run")
	if code != ExitUsage || !strings.Contains(errOut, "in no sweep preset") {
		t.Errorf("code %d, stderr %q", code, errOut)
	}
}

// TestPresetArgument pins the positional preset: unknown names are usage
// errors naming the valid ones, legacy names run everything with a note.
func TestPresetArgument(t *testing.T) {
	newCleanupFixture(t, nil)
	code, _, errOut := brooom(t, "", "sweep", "reckless", "--dry-run")
	if code != ExitUsage || !strings.Contains(errOut, "after-agents, tidy, everything") {
		t.Errorf("unknown preset: code %d, stderr %q", code, errOut)
	}
	code, _, errOut = brooom(t, "", "sweep", "safe", "--dry-run")
	if code != ExitOK || !strings.Contains(errOut, `the preset "safe" was renamed; running "everything"`) {
		t.Errorf("legacy preset: code %d, stderr %q", code, errOut)
	}
	code, _, errOut = brooom(t, "", "sweep", "tidy", "everything")
	if code != ExitUsage {
		t.Errorf("two presets: code %d, stderr %q", code, errOut)
	}
}

// TestShortcutDetectorsAreRegistered pins the assumption behind #238: every
// detector the config knows is linked in, so sweep has no "not available in
// this build" error path.
func TestShortcutDetectorsAreRegistered(t *testing.T) {
	for _, name := range config.DetectorNames() {
		if _, ok := detect.Get(name); !ok {
			t.Errorf("detector %q is known to the config but not registered", name)
		}
	}
}

// TestRemovedCommandsAreUnknown: the noun commands were folded into sweep
// (#286); calling one is an unknown-command usage error.
func TestRemovedCommandsAreUnknown(t *testing.T) {
	isolate(t)
	for _, name := range []string{"branches", "worktrees", "logs", "artifacts", "ai"} {
		code, _, errOut := brooom(t, "", name)
		if code != ExitUsage || !strings.Contains(errOut, "unknown command") {
			t.Errorf("%s: code %d, stderr %q", name, code, errOut)
		}
	}
}

func TestDisabledDetectorsAreSkipped(t *testing.T) {
	cfg := map[string]any{"detectors": map[string]any{"merged-branch": map[string]any{"enabled": false}}}
	f := newCleanupFixture(t, cfg)
	f.mergedAndSquashed()
	code, out, errOut := brooom(t, "", sweepArgs("--dry-run", "--verbose")...)
	if code != ExitOK || !strings.Contains(out, "nothing to clean") {
		t.Errorf("code %d, stdout %q", code, out)
	}
	if !strings.Contains(errOut, "skipping detector merged-branch") {
		t.Errorf("no verbose note: %q", errOut)
	}
	code, out, _ = brooom(t, "", sweepArgs("--format", "json")...)
	if code != ExitOK || !json.Valid([]byte(out)) {
		t.Errorf("json with nothing selected: code %d, stdout %q", code, out)
	}
}

func TestInvalidTrashStrategy(t *testing.T) {
	newCleanupFixture(t, nil)
	code, _, errOut := brooom(t, "", "sweep", "--dry-run", "--trash-strategy", "shred")
	if code != ExitUsage || !strings.Contains(errOut, "trash, quarantine, delete") {
		t.Errorf("code %d, stderr %q", code, errOut)
	}
}

func TestUnwritableSessionsDirFailsBeforeChanging(t *testing.T) {
	f := newCleanupFixture(t, nil)
	f.mergedAndSquashed()
	// A file where the sessions directory should be makes the manifest
	// impossible to create.
	if err := os.WriteFile(filepath.Join(f.home, "sessions"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, errOut := brooom(t, "", sweepArgs("--yes")...)
	if code != ExitError || !strings.Contains(errOut, "nothing was changed") {
		t.Errorf("code %d, stderr %q", code, errOut)
	}
	if !f.hasBranch("feat/merged") {
		t.Errorf("a branch was deleted although the manifest could not be written")
	}
}

func TestScanFooterPointsToSweep(t *testing.T) {
	f := newCleanupFixture(t, nil)
	f.mergedAndSquashed()
	code, out, _ := brooom(t, "", "scan", "--detector", "merged-branch")
	if code != ExitOK || !strings.Contains(out, "nothing was changed; run `brooom sweep --detector merged-branch` to review and clean these") {
		t.Errorf("table footer: code %d\n%s", code, out)
	}
	_, out, _ = brooom(t, "", "scan", "--detector", "merged-branch", "--format", "json")
	if strings.Contains(out, "nothing was changed") {
		t.Errorf("json output has a footer")
	}
}

func TestSelectionFlagsPassThrough(t *testing.T) {
	isolate(t)
	a := &app{}
	req, err := a.newScanRequest(scanOptions{userLocations: true})
	if err != nil {
		t.Fatal(err)
	}
	if !req.cfg.Detectors.AIArtifacts.UserLocations {
		t.Error("userLocations did not reach the config")
	}
	req, err = a.newScanRequest(scanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if req.cfg.Detectors.AIArtifacts.UserLocations {
		t.Error("user locations are on without the flag")
	}
}

// TestUserLocationsTargetTheSelectedDetector: userLocations enables the user
// locations of log-and-runtime-files only when logs are selected, those of
// ai-artifacts only when ai is selected, and neither is on without it.
func TestUserLocationsTargetTheSelectedDetector(t *testing.T) {
	cases := []struct {
		name         string
		opts         scanOptions
		wantAI, logs bool
	}{
		{"logs with user locations", scanOptions{detectors: []string{config.DetectorLogs}, userLocations: true}, false, true},
		{"logs without", scanOptions{detectors: []string{config.DetectorLogs}}, false, false},
		{"ai with user locations", scanOptions{detectors: []string{config.DetectorAIArtifacts}, userLocations: true}, true, false},
		{"ai without", scanOptions{detectors: []string{config.DetectorAIArtifacts}}, false, false},
		{"no selection keeps the ai behaviour", scanOptions{userLocations: true}, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			isolate(t)
			req, err := (&app{}).newScanRequest(tc.opts)
			if err != nil {
				t.Fatal(err)
			}
			d := req.cfg.Detectors
			if d.AIArtifacts.UserLocations != tc.wantAI || d.Logs.UserLocations != tc.logs {
				t.Errorf("ai=%v logs=%v, want ai=%v logs=%v", d.AIArtifacts.UserLocations, d.Logs.UserLocations, tc.wantAI, tc.logs)
			}
		})
	}
}

func TestCommandLineQuoting(t *testing.T) {
	tests := []struct{ goos, want string }{
		{"linux", `brooom sweep --config '/tmp/my dir/c.json' ''`},
		{"windows", `brooom sweep --config "/tmp/my dir/c.json" ""`},
	}
	for _, tt := range tests {
		a := &app{goos: tt.goos, args: []string{"sweep", "--config", "/tmp/my dir/c.json", ""}}
		if got := a.commandLine(); got != tt.want {
			t.Errorf("%s: got %q, want %q", tt.goos, got, tt.want)
		}
	}
}
