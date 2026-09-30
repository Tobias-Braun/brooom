package action

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/scope"
	"github.com/Tobias-Braun/brooom/internal/session"
	"github.com/Tobias-Braun/brooom/internal/testutil"
	"github.com/Tobias-Braun/brooom/internal/trash"
)

// undoFake is a scriptable action whose Undo outcome is looked up by the
// entry path, so one test can mix successes, conflicts and failures.
const undoFakeType findings.ActionType = "undo-fake"

type undoFake struct{}

var (
	undoOutcome = map[string]func(session.Entry) error{}
	undoLog     []string
)

func (undoFake) Type() findings.ActionType { return undoFakeType }
func (undoFake) Plan(context.Context, *Env, findings.Finding) (Step, error) {
	return Step{}, errors.New("not used")
}
func (undoFake) Apply(context.Context, *Env, Step) (session.Entry, error) {
	return session.Entry{}, errors.New("not used")
}
func (undoFake) Undo(_ context.Context, _ *Env, e session.Entry) error {
	undoLog = append(undoLog, e.Path)
	if fn := undoOutcome[e.Path]; fn != nil {
		return fn(e)
	}
	return nil
}

func init() { Register(undoFake{}) }

// undoFixture is a scope root with a guard, a session store and a manifest of
// fake entries.
type undoFixture struct {
	t     *testing.T
	root  string
	env   *Env
	store *session.Store
	out   strings.Builder
}

func newUndoFixture(t *testing.T) *undoFixture {
	t.Helper()
	undoLog = nil
	clear(undoOutcome)
	root := testutil.ResolvedTempDir(t)
	guard, err := scope.NewGuard(root)
	if err != nil {
		t.Fatal(err)
	}
	return &undoFixture{t: t, root: root, env: &Env{Guard: guard},
		store: session.NewStore(filepath.Join(t.TempDir(), "sessions"))}
}

func (fx *undoFixture) entry(name string) session.Entry {
	return session.Entry{Action: undoFakeType, Path: filepath.Join(fx.root, name), Status: session.StatusApplied,
		Restorable: true, SizeBytes: 10}
}

func (fx *undoFixture) manifest(entries ...session.Entry) *session.Manifest {
	m := &session.Manifest{ID: "20260930-120000-abcd", Command: "brooom sweep --apply"}
	for _, e := range entries {
		m.Add(e)
	}
	if err := fx.store.Save(m); err != nil {
		fx.t.Fatal(err)
	}
	return m
}

func (fx *undoFixture) run(m *session.Manifest, opts UndoOptions) (*UndoResult, error) {
	fx.t.Helper()
	fx.out.Reset()
	opts.IO = IO{In: strings.NewReader(""), Out: &fx.out}
	opts.Store = fx.store
	return RunUndo(context.Background(), fx.env, m, opts)
}

func TestPlanUndoReverseOrderAndKinds(t *testing.T) {
	fx := newUndoFixture(t)
	done := fx.entry("done")
	done.Status = session.StatusRestored
	failed := fx.entry("failed")
	failed.Status, failed.Error = session.StatusFailed, "boom"
	gc := fx.entry("gc")
	gc.Action = findings.ActionGitGC
	del := fx.entry("del")
	del.Trash = &trash.Record{Strategy: config.StrategyDelete, OriginalPath: filepath.Join(fx.root, "del")}
	unknown := fx.entry("unknown")
	unknown.Action = "from-the-future"
	plain := fx.entry("plain")
	plain.Restorable, plain.RecoveryHint = false, "do it by hand"
	ok := fx.entry("ok")
	m := fx.manifest(ok, plain, unknown, del, gc, failed, done)

	steps := PlanUndo(m, fx.env)
	if len(steps) != 7 || steps[0].Entry.Path != done.Path || steps[6].Entry.Path != ok.Path {
		t.Fatalf("steps are not in reverse order: %+v", steps)
	}
	want := map[string]struct {
		kind UndoKind
		text string
	}{
		"done":    {UndoDone, "already restored"},
		"failed":  {UndoCannot, "failed"},
		"gc":      {UndoCannot, "git maintenance"},
		"del":     {UndoCannot, "delete strategy"},
		"unknown": {UndoCannot, "unknown action type"},
		"plain":   {UndoCannot, "not restorable"},
		"ok":      {UndoRestore, ""},
	}
	for _, s := range steps {
		w := want[filepath.Base(s.Entry.Path)]
		if s.Kind != w.kind || !strings.Contains(s.Reason, w.text) {
			t.Errorf("%s: kind=%s reason=%q, want %s containing %q", s.Entry.Path, s.Kind, s.Reason, w.kind, w.text)
		}
	}
}

func TestRunUndoDryRunRestoresNothing(t *testing.T) {
	fx := newUndoFixture(t)
	m := fx.manifest(fx.entry("a"), fx.entry("b"))
	res, err := fx.run(m, UndoOptions{RerunHint: "re-run 'brooom undo --apply'"})
	if err != nil || len(undoLog) != 0 || res.Restored != 0 {
		t.Fatalf("dry run changed something: err=%v log=%v res=%+v", err, undoLog, res)
	}
	out := fx.out.String()
	if !strings.Contains(out, "dry run: nothing was restored; re-run 'brooom undo --apply' to restore") ||
		strings.Index(out, "/b") > strings.Index(out, "/a") {
		t.Fatalf("output %q", out)
	}
}

func TestRunUndoSavesManifestAfterEveryEntry(t *testing.T) {
	fx := newUndoFixture(t)
	m := fx.manifest(fx.entry("first"), fx.entry("second"))
	// Entries are undone last first, so "second" runs before "first"; when
	// "first" runs, "second" must already be persisted as restored.
	undoOutcome[filepath.Join(fx.root, "first")] = func(session.Entry) error {
		saved, err := fx.store.Load(m.ID)
		if err != nil {
			return err
		}
		if saved.Entries[1].Status != session.StatusRestored || saved.ReclaimedBytes != 10 {
			return fmt.Errorf("manifest not saved after the first restore: %+v", saved)
		}
		return nil
	}
	res, err := fx.run(m, UndoOptions{Apply: true, Yes: true})
	if err != nil || res.Restored != 2 || res.Incomplete() {
		t.Fatalf("err=%v res=%+v out=%s", err, res, fx.out.String())
	}
	if strings.Join(undoLog, ",") != filepath.Join(fx.root, "second")+","+filepath.Join(fx.root, "first") {
		t.Fatalf("order %v", undoLog)
	}
	saved, _ := fx.store.Load(m.ID)
	if saved.ReclaimedBytes != 0 || saved.Entries[0].Status != session.StatusRestored {
		t.Fatalf("final manifest %+v", saved)
	}
	// A second run is idempotent: everything is skipped as already restored.
	undoLog = nil
	res, err = fx.run(saved, UndoOptions{Apply: true, Yes: true})
	if err != nil || len(undoLog) != 0 || res.AlreadyRestored != 2 || res.Restored != 0 {
		t.Fatalf("second run: err=%v log=%v res=%+v", err, undoLog, res)
	}
}

func TestRunUndoConflictsAndFailuresContinue(t *testing.T) {
	fx := newUndoFixture(t)
	m := fx.manifest(fx.entry("ok"), fx.entry("conflict"), fx.entry("branch"), fx.entry("gone"), fx.entry("bad"))
	undoOutcome[filepath.Join(fx.root, "conflict")] = func(session.Entry) error { return fmt.Errorf("x: %w", trash.ErrRestoreConflict) }
	undoOutcome[filepath.Join(fx.root, "branch")] = func(session.Entry) error { return &undoConflictError{"branch feat/x already exists at abc"} }
	undoOutcome[filepath.Join(fx.root, "gone")] = func(session.Entry) error { return fmt.Errorf("y: %w", trash.ErrNotRestorable) }
	undoOutcome[filepath.Join(fx.root, "bad")] = func(session.Entry) error { return errors.New("kaboom") }

	res, err := fx.run(m, UndoOptions{Apply: true, Yes: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Restored != 1 || res.Conflicts != 2 || res.Failed != 2 || !res.Incomplete() {
		t.Fatalf("res=%+v", res)
	}
	saved, _ := fx.store.Load(m.ID)
	for i, want := range []session.Status{session.StatusRestored, session.StatusApplied, session.StatusApplied, session.StatusApplied, session.StatusApplied} {
		if saved.Entries[i].Status != want {
			t.Errorf("entry %d status %s, want %s", i, saved.Entries[i].Status, want)
		}
	}
	for _, s := range []string{"conflict ", "kaboom", "already exists at abc", "summary: 1 restored, 2 conflicts, 2 failed"} {
		if !strings.Contains(fx.out.String(), s) {
			t.Errorf("output lacks %q:\n%s", s, fx.out.String())
		}
	}
}

func TestRunUndoNonRestorableDoNotFailTheRun(t *testing.T) {
	fx := newUndoFixture(t)
	plain := fx.entry("plain")
	plain.Restorable, plain.RecoveryHint = false, "git branch x abc"
	m := fx.manifest(fx.entry("ok"), plain)
	res, err := fx.run(m, UndoOptions{Apply: true, Yes: true})
	if err != nil || res.Incomplete() || res.NotRestorable != 1 || res.Restored != 1 {
		t.Fatalf("err=%v res=%+v", err, res)
	}
	if !strings.Contains(fx.out.String(), "recovery: git branch x abc") {
		t.Fatalf("hint missing:\n%s", fx.out.String())
	}
}

func TestRunUndoConfirmation(t *testing.T) {
	tests := []struct {
		name     string
		in       string
		tty      bool
		wantErr  error
		restored int
		declined bool
	}{
		{"yes", "y\n", true, nil, 1, false},
		{"full word, CRLF", "YES\r\n", true, nil, 1, false},
		{"no", "n\n", true, nil, 0, true},
		{"empty answer", "\n", true, nil, 0, true},
		{"closed stdin", "", true, nil, 0, true},
		{"not a terminal", "y\n", false, ErrConfirmationRequired, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fx := newUndoFixture(t)
			m := fx.manifest(fx.entry("a"))
			var out strings.Builder
			res, err := RunUndo(context.Background(), fx.env, m, UndoOptions{
				Apply: true, Store: fx.store, StdinIsTTY: func() bool { return tt.tty },
				IO: IO{In: strings.NewReader(tt.in), Out: &out},
			})
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if tt.wantErr != nil {
				if len(undoLog) != 0 || out.Len() != 0 {
					t.Fatalf("something happened before the refusal: %v %q", undoLog, out.String())
				}
				return
			}
			if res.Restored != tt.restored || res.Declined != tt.declined {
				t.Fatalf("res=%+v", res)
			}
			if tt.tty && !strings.Contains(out.String(), "Restore 1 item? [y/N] ") {
				t.Fatalf("prompt missing: %q", out.String())
			}
		})
	}
}

func TestPlanUndoScopeGuard(t *testing.T) {
	fx := newUndoFixture(t)
	outside := testutil.ResolvedTempDir(t)
	inside := fx.entry("in")
	out := fx.entry("out")
	out.Path = filepath.Join(outside, "out")
	trashed := session.Entry{Action: undoFakeType, Path: filepath.Join(outside, "t"), Status: session.StatusApplied, Restorable: true,
		Trash: &trash.Record{Strategy: config.StrategyQuarantine, OriginalPath: filepath.Join(outside, "t")}}
	wt := session.Entry{Action: undoFakeType, Path: filepath.Join(outside, "w"), Status: session.StatusApplied, Restorable: true,
		Undo: map[string]string{"worktree": filepath.Join(fx.root, "wt"), "repo": outside}}
	m := fx.manifest(inside, out, trashed, wt)
	res, err := fx.run(m, UndoOptions{Apply: true, Yes: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Restored != 1 || res.NotRestorable != 0 || res.OutsideScope != 3 || len(undoLog) != 1 || undoLog[0] != inside.Path {
		t.Fatalf("res=%+v log=%v", res, undoLog)
	}
	for _, s := range res.Steps {
		if s.Entry.Path != inside.Path && (!s.OutsideScope || !strings.Contains(s.Reason, "outside the current scope; re-run from ")) {
			t.Errorf("%s: %+v", s.Entry.Path, s)
		}
	}
	if !strings.Contains(fx.out.String(), "or with --path") {
		t.Fatalf("scope hint missing:\n%s", fx.out.String())
	}
}

func TestPlanUndoWithoutGuardRefusesEverything(t *testing.T) {
	fx := newUndoFixture(t)
	fx.env.Guard = nil
	steps := PlanUndo(fx.manifest(fx.entry("a")), fx.env)
	if steps[0].Kind != UndoOutside || !steps[0].OutsideScope {
		t.Fatalf("%+v", steps[0])
	}
}

func TestPlanUndoTrashEntries(t *testing.T) {
	fx := newUndoFixture(t)
	stored := filepath.Join(fx.root, "store", "1", "f")
	orig := filepath.Join(fx.root, "orig")
	mk := func(name, storedPath string) session.Entry {
		return session.Entry{Action: findings.ActionTrash, Path: filepath.Join(fx.root, name), Status: session.StatusApplied,
			Restorable: true, Trash: &trash.Record{Strategy: config.StrategyQuarantine, Restorable: true,
				OriginalPath: filepath.Join(fx.root, name), StoredPath: storedPath}}
	}
	if err := os.MkdirAll(filepath.Dir(stored), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stored, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(orig, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	steps := PlanUndo(fx.manifest(mk("fine", stored), mk("orig", stored), mk("lost", filepath.Join(fx.root, "nope"))), fx.env)
	got := map[string]UndoKind{}
	for _, s := range steps {
		got[filepath.Base(s.Entry.Path)] = s.Kind
	}
	if got["fine"] != UndoRestore || got["orig"] != UndoConflict || got["lost"] != UndoCannot {
		t.Fatalf("kinds %v", got)
	}
	for _, s := range steps {
		if filepath.Base(s.Entry.Path) == "lost" && !strings.Contains(s.Reason, "is gone") {
			t.Errorf("reason %q", s.Reason)
		}
		if filepath.Base(s.Entry.Path) == "fine" && s.Description != "restore "+s.Entry.Path+" from "+stored {
			t.Errorf("description %q", s.Description)
		}
	}
}

func TestDescribeUndo(t *testing.T) {
	tests := []struct {
		name string
		e    session.Entry
		want string
	}{
		{"branch", session.Entry{Action: findings.ActionDeleteBranch, Path: "/r", Undo: map[string]string{"branch": "feat/x", "sha": "1a2b3c4d5e6f"}},
			"recreate branch feat/x at 1a2b3c4 (/r)"},
		{"worktree", session.Entry{Action: findings.ActionRemoveWorktree, Undo: map[string]string{"worktree": "/w", "branch": "b"}},
			"re-add worktree /w"},
		{"trash without stored path", session.Entry{Action: findings.ActionTrash, Trash: &trash.Record{OriginalPath: "/p"}},
			"restore /p from trash"},
		{"other", session.Entry{Action: "x", Path: "/p"}, "undo x /p"},
	}
	for _, tt := range tests {
		if got := describeUndo(tt.e); got != tt.want {
			t.Errorf("%s: %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestUndoConflictErrorMatchesRestoreConflict(t *testing.T) {
	var err error = &undoConflictError{"branch exists"}
	if !errors.Is(err, trash.ErrRestoreConflict) || err.Error() != "branch exists" {
		t.Fatalf("%v", err)
	}
}
