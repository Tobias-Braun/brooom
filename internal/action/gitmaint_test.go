package action

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/gitx"
	"github.com/Tobias-Braun/brooom/internal/session"
	"github.com/Tobias-Braun/brooom/internal/testutil"
	"github.com/Tobias-Braun/brooom/internal/trash"
)

// maintFixture reuses the worktree fixture: a real repository, guard rooted at
// it and the real git runner.
type maintFixture struct {
	*wtFixture
}

func newMaintFixture(t *testing.T) *maintFixture {
	t.Helper()
	return &maintFixture{newWTFixture(t)}
}

// finding builds the finding the git-bloat detector or the CLI would emit.
func (fx *maintFixture) finding(typ findings.ActionType, args map[string]string) findings.Finding {
	return findings.Finding{
		ID:              "id-" + string(typ),
		Detector:        "git-bloat",
		Path:            fx.repo.Dir,
		Kind:            findings.KindGitObjects,
		SuggestedAction: findings.SuggestedAction{Type: typ, Args: args},
	}
}

func act(t *testing.T, typ findings.ActionType) Action {
	t.Helper()
	a, ok := Get(typ)
	if !ok {
		t.Fatalf("action %s is not registered", typ)
	}
	return a
}

// dangling writes an unreachable blob and returns its id.
func (fx *maintFixture) dangling(content string) string {
	fx.t.Helper()
	p := fx.repo.WriteFile("dangling.tmp", content)
	id := fx.repo.Git("hash-object", "-w", "dangling.tmp")
	if err := os.Remove(p); err != nil {
		fx.t.Fatal(err)
	}
	return id
}

// hasObject reports whether the object exists in the repository.
func (fx *maintFixture) hasObject(id string) bool {
	fx.t.Helper()
	_, err := fx.git.Run(context.Background(), fx.repo.Dir, "cat-file", "-e", id)
	return err == nil
}

// looseCommits makes n commits with a file each, all reachable and loose.
func (fx *maintFixture) looseCommits(n int) {
	fx.t.Helper()
	for i := range n {
		fx.repo.WriteFile(fmt.Sprintf("f%02d.txt", i), strings.Repeat("x", 100+i))
		fx.repo.CommitAll(fmt.Sprintf("c%d", i), testutil.BaseTime.Add(time.Duration(i+1)*time.Minute))
	}
}

// loose returns the loose object count.
func (fx *maintFixture) loose() int64 {
	fx.t.Helper()
	s, err := gitx.CountObjects(context.Background(), fx.git, fx.repo.Dir)
	if err != nil {
		fx.t.Fatal(err)
	}
	return s.Count
}

// reflogLines returns the number of HEAD reflog entries.
func (fx *maintFixture) reflogLines() int {
	fx.t.Helper()
	return len(gitx.Lines(fx.repo.Git("reflog", "show", "HEAD")))
}

// snapshot records path and size of every file below .git.
func (fx *maintFixture) snapshot() map[string]int64 {
	fx.t.Helper()
	out := map[string]int64{}
	root := filepath.Join(fx.repo.Dir, ".git")
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
		fx.t.Fatal(err)
	}
	return out
}

func mustPlan(t *testing.T, fx *maintFixture, f findings.Finding) Step {
	t.Helper()
	s, err := act(t, f.SuggestedAction.Type).Plan(context.Background(), fx.env, f)
	if err != nil {
		t.Fatalf("Plan(%s): %v", f.SuggestedAction.Type, err)
	}
	return s
}

func wantMaintSkip(t *testing.T, err error, contains string) {
	t.Helper()
	if !errors.Is(err, ErrSkipped) {
		t.Fatalf("err = %v, want an ErrSkipped error", err)
	}
	if !strings.Contains(err.Error(), contains) {
		t.Fatalf("err = %q, want it to contain %q", err, contains)
	}
}

func TestGitGCPacksLooseObjectsAndMeasures(t *testing.T) {
	fx := newMaintFixture(t)
	fx.looseCommits(30)
	before := fx.loose()
	if before < 30 {
		t.Fatalf("setup: %d loose objects", before)
	}
	f := fx.finding(findings.ActionGitGC, map[string]string{"prune": "now"})
	step := mustPlan(t, fx, f)
	for _, want := range []string{"git gc", "NOT restorable", "gc.reflogExpire", "loose objects"} {
		if !strings.Contains(step.Description, want) {
			t.Errorf("description %q lacks %q", step.Description, want)
		}
	}
	if !strings.Contains(step.Command, "gc --quiet --prune=now") {
		t.Errorf("command = %q", step.Command)
	}
	en, err := act(t, findings.ActionGitGC).Apply(context.Background(), fx.env, step)
	if err != nil {
		t.Fatal(err)
	}
	if after := fx.loose(); after >= before {
		t.Errorf("loose objects %d -> %d, want fewer", before, after)
	}
	if en.Status != session.StatusApplied || en.Restorable || len(en.Undo) != 0 || en.SizeBytes < 0 {
		t.Errorf("entry = %+v", en)
	}
	for _, want := range []string{"not restorable", "gc.reflogExpire / gc.reflogExpireUnreachable", "90 / 30 days", "unreachable objects older than now"} {
		if !strings.Contains(en.RecoveryHint, want) {
			t.Errorf("hint %q lacks %q", en.RecoveryHint, want)
		}
	}
}

func TestGitGCDefaultsPruneDateFromConfig(t *testing.T) {
	fx := newMaintFixture(t)
	f := fx.finding(findings.ActionGitGC, nil)
	step := mustPlan(t, fx, f)
	if !strings.Contains(step.Command, "--prune=2.weeks.ago") {
		t.Errorf("command = %q, want the configured default prune date", step.Command)
	}
}

func TestGitPruneRemovesDanglingObject(t *testing.T) {
	fx := newMaintFixture(t)
	id := fx.dangling("only reachable through nothing")
	f := fx.finding(findings.ActionGitPrune, map[string]string{"expire": "now"})
	step := mustPlan(t, fx, f)
	if !strings.Contains(step.Description, "1 unreachable object") || !strings.Contains(step.Description, "NOT restorable") {
		t.Errorf("description = %q", step.Description)
	}
	en, err := act(t, findings.ActionGitPrune).Apply(context.Background(), fx.env, step)
	if err != nil {
		t.Fatal(err)
	}
	if fx.hasObject(id) {
		t.Error("dangling blob survived git prune")
	}
	if en.Restorable || en.SizeBytes < 0 || !strings.Contains(en.RecoveryHint, "unreachable objects older than now were deleted for good") {
		t.Errorf("entry = %+v", en)
	}
}

func TestGitPruneYoungObjectsAreNothingToDo(t *testing.T) {
	fx := newMaintFixture(t)
	id := fx.dangling("fresh")
	f := fx.finding(findings.ActionGitPrune, map[string]string{"expire": "2.weeks.ago"})
	_, err := act(t, findings.ActionGitPrune).Plan(context.Background(), fx.env, f)
	wantMaintSkip(t, err, "nothing to do")
	if !fx.hasObject(id) {
		t.Error("object was removed by a plan")
	}
}

func TestGitReflogExpireRemovesEntriesAndRecoveryPoints(t *testing.T) {
	fx := newMaintFixture(t)
	fx.repo.Git("checkout", "-q", "-b", "feat")
	fx.repo.WriteFile("feat.txt", "work")
	fx.repo.CommitAll("feature work", testutil.BaseTime.Add(time.Hour))
	tip := fx.repo.Head()
	fx.repo.Checkout("main")
	fx.repo.Git("branch", "-D", "feat")
	if !strings.Contains(fx.repo.Git("reflog", "show", "HEAD"), tip[:7]) {
		t.Fatal("setup: the deleted branch tip must be recoverable through the reflog")
	}
	before := fx.reflogLines()
	f := fx.finding(findings.ActionGitReflogExpire, map[string]string{"expire": "now"})
	step := mustPlan(t, fx, f)
	if !strings.Contains(step.Description, "reflog entr") || !strings.Contains(step.Description, "NOT restorable") {
		t.Errorf("description = %q", step.Description)
	}
	en, err := act(t, findings.ActionGitReflogExpire).Apply(context.Background(), fx.env, step)
	if err != nil {
		t.Fatal(err)
	}
	if after := fx.reflogLines(); after >= before {
		t.Errorf("reflog lines %d -> %d", before, after)
	}
	if strings.Contains(fx.repo.Git("reflog", "show", "HEAD"), tip[:7]) {
		t.Error("the deleted branch tip is still listed in the reflog")
	}
	if en.Restorable || !strings.Contains(en.RecoveryHint, "reflog entries older than now were removed") {
		t.Errorf("entry = %+v", en)
	}
	if en.SizeBytes <= 0 {
		t.Errorf("SizeBytes = %d, want the measured reflog shrinkage", en.SizeBytes)
	}
}

func TestGitReflogExpireRecentEntriesAreNothingToDo(t *testing.T) {
	fx := newMaintFixture(t)
	fx.repo.Git("commit", "-q", "--allow-empty", "-m", "x")
	f := fx.finding(findings.ActionGitReflogExpire, map[string]string{"expire": "1.year.ago"})
	_, err := act(t, findings.ActionGitReflogExpire).Plan(context.Background(), fx.env, f)
	wantMaintSkip(t, err, "nothing to do")
}

func TestMaintenanceRejectsInvalidDatesWithoutChange(t *testing.T) {
	dates := []string{"garbage", "--all", "-1", "", "   ", "!!!", ";;", "now\nrm", "$(true)!"}
	for _, typ := range []findings.ActionType{findings.ActionGitPrune, findings.ActionGitReflogExpire, findings.ActionGitGC} {
		arg := "expire"
		if typ == findings.ActionGitGC {
			arg = "prune"
		}
		for _, d := range dates {
			t.Run(fmt.Sprintf("%s/%q", typ, d), func(t *testing.T) {
				fx := newMaintFixture(t)
				fx.dangling("x")
				snap := fx.snapshot()
				f := fx.finding(typ, map[string]string{arg: d})
				_, err := act(t, typ).Plan(context.Background(), fx.env, f)
				wantMaintSkip(t, err, "invalid git date")
				if d := diffSnapshots(snap, fx.snapshot()); d != "" {
					t.Errorf("a rejected date changed the repository: %s", d)
				}
			})
		}
	}
}

// diffSnapshots names every file whose presence or size differs between two
// snapshots, or returns "" when they are equal. Naming the file is the point:
// "the repository changed" alone made the CI failure of PR #157 impossible to
// attribute.
func diffSnapshots(before, after map[string]int64) string {
	var diffs []string
	for k, v := range before {
		switch w, ok := after[k]; {
		case !ok:
			diffs = append(diffs, fmt.Sprintf("%s vanished", k))
		case w != v:
			diffs = append(diffs, fmt.Sprintf("%s %d -> %d bytes", k, v, w))
		}
	}
	for k := range after {
		if _, ok := before[k]; !ok {
			diffs = append(diffs, fmt.Sprintf("%s appeared", k))
		}
	}
	slices.Sort(diffs)
	return strings.Join(diffs, "; ")
}

func TestValidateDate(t *testing.T) {
	fx := newMaintFixture(t)
	ctx := context.Background()
	for _, ok := range []string{"now", "90.days.ago", "2026-01-01", "2.weeks.ago"} {
		if err := ValidateDate(ctx, fx.git, fx.repo.Dir, ok); err != nil {
			t.Errorf("ValidateDate(%q) = %v", ok, err)
		}
	}
	err := ValidateDate(ctx, fx.git, fx.repo.Dir, "garbage")
	if !errors.Is(err, ErrInvalidDate) || !strings.Contains(err.Error(), "garbage") {
		t.Errorf("garbage: %v", err)
	}
	if err := CheckDateSyntax("-x"); !errors.Is(err, ErrInvalidDate) {
		t.Errorf("CheckDateSyntax(-x) = %v", err)
	}
}

func TestMaintenanceSkipsMidOperation(t *testing.T) {
	markers := []struct {
		name string
		dir  bool
	}{
		{"rebase-merge", true}, {"rebase-apply", true}, {"MERGE_HEAD", false},
		{"CHERRY_PICK_HEAD", false}, {"REVERT_HEAD", false}, {"BISECT_LOG", false},
	}
	types := map[findings.ActionType]map[string]string{
		findings.ActionGitPrune:        {"expire": "now"},
		findings.ActionGitReflogExpire: {"expire": "now"},
		findings.ActionGitGC:           {"prune": "now"},
	}
	for typ, args := range types {
		for _, m := range markers {
			t.Run(string(typ)+"/"+m.name, func(t *testing.T) {
				fx := newMaintFixture(t)
				fx.dangling("x")
				p := filepath.Join(fx.repo.Dir, ".git", m.name)
				var err error
				if m.dir {
					err = os.Mkdir(p, 0o755)
				} else {
					err = os.WriteFile(p, []byte("x\n"), 0o644)
				}
				if err != nil {
					t.Fatal(err)
				}
				_, err = act(t, typ).Plan(context.Background(), fx.env, fx.finding(typ, args))
				wantMaintSkip(t, err, "in progress")
			})
		}
	}
}

func TestMaintenanceSkipsWhenLinkedWorktreeIsMidOperation(t *testing.T) {
	fx := newMaintFixture(t)
	fx.repo.Branch("side")
	fx.add("wt", "side")
	entries, err := os.ReadDir(filepath.Join(fx.repo.Dir, ".git", "worktrees"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("worktree metadata: %v %v", entries, err)
	}
	if err := os.Mkdir(filepath.Join(fx.repo.Dir, ".git", "worktrees", entries[0].Name(), "rebase-merge"), 0o755); err != nil {
		t.Fatal(err)
	}
	f := fx.finding(findings.ActionGitPrune, map[string]string{"expire": "now"})
	_, err = act(t, findings.ActionGitPrune).Plan(context.Background(), fx.env, f)
	wantMaintSkip(t, err, "rebase")
}

func TestMaintenanceRefusals(t *testing.T) {
	fx := newMaintFixture(t)
	sub := filepath.Join(fx.repo.Dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := testutil.ResolvedTempDir(t)
	tests := []struct {
		name   string
		mutate func(*findings.Finding)
		want   string
	}{
		{"outside scope", func(f *findings.Finding) { f.Path = outside }, "outside the allowed scope"},
		{"not a root", func(f *findings.Finding) { f.Path = sub }, "not the root"},
		{"wrong type", func(f *findings.Finding) { f.SuggestedAction.Type = findings.ActionTrash }, "not git-prune"},
		{"blocking flag", func(f *findings.Finding) { f.RiskFlags = []findings.RiskFlag{findings.RiskUncommittedChanges} }, "blocked by risk flag"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := fx.finding(findings.ActionGitPrune, map[string]string{"expire": "now"})
			tt.mutate(&f)
			_, err := gitPrune{}.Plan(context.Background(), fx.env, f)
			wantMaintSkip(t, err, tt.want)
		})
	}
}

// failingMaintGit injects an error for one git subcommand and delegates the rest
// (including stdin input) to the real runner.
type failingMaintGit struct {
	*gitx.ExecRunner
	sub string
	err error
}

func (r failingMaintGit) Run(ctx context.Context, dir string, args ...string) (string, error) {
	if slices.Contains(args, r.sub) && !slices.Contains(args, "-n") && !slices.Contains(args, "--dry-run") {
		return "", r.err
	}
	return r.ExecRunner.Run(ctx, dir, args...)
}

func TestGitGCRunningElsewhereIsASkip(t *testing.T) {
	fx := newMaintFixture(t)
	f := fx.finding(findings.ActionGitGC, map[string]string{"prune": "now"})
	step := mustPlan(t, fx, f)
	fx.env.Git = failingMaintGit{fx.git, "gc", &gitx.Error{Args: []string{"gc"}, Dir: fx.repo.Dir, ExitCode: 128,
		Stderr: "fatal: gc is already running on machine 'box' pid 42 (use --force if not)\n"}}
	en, err := gitGC{}.Apply(context.Background(), fx.env, step)
	if err != nil || en.Status != session.StatusSkipped || !strings.Contains(en.Error, "already running") {
		t.Fatalf("entry %+v, err %v", en, err)
	}
}

func TestMaintenanceFailureNamesRepository(t *testing.T) {
	fx := newMaintFixture(t)
	f := fx.finding(findings.ActionGitGC, map[string]string{"prune": "now"})
	step := mustPlan(t, fx, f)
	fx.env.Git = failingMaintGit{fx.git, "gc", &gitx.Error{Args: []string{"gc"}, Dir: fx.repo.Dir, ExitCode: 1, Stderr: "error: unable to unlink pack: Access is denied\n"}}
	en, err := gitGC{}.Apply(context.Background(), fx.env, step)
	if err == nil || en.Status != session.StatusFailed || !strings.Contains(err.Error(), fx.repo.Dir) {
		t.Fatalf("entry %+v, err %v", en, err)
	}
}

func TestDryRunChangesNothing(t *testing.T) {
	fx := newMaintFixture(t)
	fx.looseCommits(5)
	fx.dangling("x")
	snap := fx.snapshot()
	for typ, args := range map[findings.ActionType]map[string]string{
		findings.ActionGitGC:           {"prune": "now"},
		findings.ActionGitPrune:        {"expire": "now"},
		findings.ActionGitReflogExpire: {"expire": "now"},
	} {
		mustPlan(t, fx, fx.finding(typ, args))
	}
	if d := diffSnapshots(snap, fx.snapshot()); d != "" {
		t.Errorf("planning changed the repository: %s", d)
	}
}

func TestMaintenanceUndoIsNotRestorable(t *testing.T) {
	for _, typ := range []findings.ActionType{findings.ActionGitGC, findings.ActionGitPrune, findings.ActionGitReflogExpire} {
		err := act(t, typ).Undo(context.Background(), &Env{}, session.Entry{Action: typ})
		if !errors.Is(err, trash.ErrNotRestorable) || !strings.Contains(err.Error(), "older than") {
			t.Errorf("%s: Undo err = %v", typ, err)
		}
	}
}

func TestExecutorRunsMaintenanceInPriorityOrder(t *testing.T) {
	fx := newMaintFixture(t)
	fx.repo.WriteFile("a.txt", "a")
	fx.repo.CommitAll("second", testutil.BaseTime.Add(time.Hour))
	fx.dangling("gone soon")
	// Listed in the wrong order on purpose: the executor sorts them.
	in := []findings.Finding{
		fx.finding(findings.ActionGitGC, map[string]string{"prune": "now"}),
		fx.finding(findings.ActionGitPrune, map[string]string{"expire": "now"}),
		fx.finding(findings.ActionGitReflogExpire, map[string]string{"expire": "now"}),
	}
	for i := range in {
		in[i].ID += fmt.Sprint(i)
		in[i].Ref = string(in[i].SuggestedAction.Type)
	}
	var out bytes.Buffer
	ex := NewExecutor(Options{
		Apply: true, Yes: true, Env: fx.env, Store: session.NewStore(filepath.Join(testutil.ResolvedTempDir(t), "sessions")),
		IO: IO{Out: &out, Err: &out},
	})
	plan := ex.Plan(context.Background(), in)
	var planned []findings.ActionType
	for _, g := range plan.Groups {
		planned = append(planned, g.Action)
	}
	want := []findings.ActionType{findings.ActionGitReflogExpire, findings.ActionGitPrune, findings.ActionGitGC}
	if !slices.Equal(planned, want) {
		t.Fatalf("plan order %v, want %v", planned, want)
	}
	res, err := ex.Run(context.Background(), in)
	if err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	var ran []findings.ActionType
	for _, e := range res.Entries {
		ran = append(ran, e.Action)
		if e.Restorable {
			t.Errorf("%s entry is restorable", e.Action)
		}
	}
	if !slices.Equal(ran, want) {
		t.Fatalf("execution order %v, want %v\n%s", ran, want, out.String())
	}
	if !strings.Contains(out.String(), "NOT restorable") {
		t.Errorf("plan output lacks the not-restorable note:\n%s", out.String())
	}
}
