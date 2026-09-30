package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/session"
	"github.com/Tobias-Braun/brooom/internal/testutil"
	"github.com/Tobias-Braun/brooom/internal/trash"
)

// undoFixture is a repository (the scope), an isolated Brooom home and a
// helper that quarantines real files and records them like the trash action
// does, so undo runs against genuine quarantine data.
type undoFixture struct {
	t     *testing.T
	repo  *testutil.Repo
	home  string
	store *session.Store
}

func newUndoFixture(t *testing.T) *undoFixture {
	t.Helper()
	needGit(t)
	home := isolate(t)
	repo := testutil.NewRepo(t)
	t.Chdir(repo.Dir)
	return &undoFixture{t: t, repo: repo, home: home, store: session.NewStore(filepath.Join(home, "sessions"))}
}

func (f *undoFixture) quarantineDir() string { return filepath.Join(f.home, "quarantine") }

func (f *undoFixture) write(rel, content string) string {
	return testutil.WriteFile(f.t, f.repo.Dir, rel, content)
}

// session quarantines the paths in order and saves the manifest.
func (f *undoFixture) session(id string, started time.Time, paths ...string) *session.Manifest {
	f.t.Helper()
	tr, err := trash.New(config.StrategyQuarantine, trash.Options{SessionID: id, QuarantineDir: f.quarantineDir()})
	if err != nil {
		f.t.Fatal(err)
	}
	m := &session.Manifest{ID: id, StartedAt: started, Command: "brooom sweep --apply"}
	for _, p := range paths {
		rec, err := tr.Remove(context.Background(), p)
		if err != nil {
			f.t.Fatal(err)
		}
		m.Add(session.Entry{Action: findings.ActionTrash, Detector: "build-artifacts", Path: p, SizeBytes: rec.SizeBytes,
			Status: session.StatusApplied, Trash: &rec, Restorable: true})
	}
	m.Finish(started.Add(time.Second))
	if err := f.store.Save(m); err != nil {
		f.t.Fatal(err)
	}
	return m
}

func (f *undoFixture) reload(id string) *session.Manifest {
	f.t.Helper()
	m, err := f.store.Load(id)
	if err != nil {
		f.t.Fatal(err)
	}
	return m
}

// runApp executes the CLI with a scripted stdin. tty says whether the fake
// stdin counts as a terminal.
func runApp(t *testing.T, stdin string, tty bool, now time.Time, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	a := &app{io: IO{In: strings.NewReader(stdin), Out: &out, Err: &errOut}, stdinTTY: func() bool { return tty }}
	if !now.IsZero() {
		a.clock = func() time.Time { return now }
	}
	code := execute(a, args)
	return code, out.String(), errOut.String()
}

func fileContent(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

const sid1 = "20260930-100000-aaaa"

func TestUndoNoSessions(t *testing.T) {
	newUndoFixture(t)
	code, out, _ := runApp(t, "", false, time.Time{}, "undo")
	if code != ExitOK || strings.TrimSpace(out) != "nothing to undo" {
		t.Fatalf("code=%d out=%q", code, out)
	}
}

func TestUndoUnknownAndAmbiguousID(t *testing.T) {
	f := newUndoFixture(t)
	f.session("20260930-100000-aaa1", time.Now(), f.write("a.txt", "a"))
	f.session("20260930-100001-aaa2", time.Now(), f.write("b.txt", "b"))
	code, _, errOut := runApp(t, "", false, time.Time{}, "undo", "zzz")
	if code != ExitError || !strings.Contains(errOut, "session not found") || !strings.Contains(errOut, "20260930-100000-aaa1") {
		t.Fatalf("unknown: code=%d err=%q", code, errOut)
	}
	code, _, errOut = runApp(t, "", false, time.Time{}, "undo", "20260930-1000")
	if code != ExitError || !strings.Contains(errOut, "ambiguous") || !strings.Contains(errOut, "aaa2") {
		t.Fatalf("ambiguous: code=%d err=%q", code, errOut)
	}
}

func TestUndoDryRunListsReverseOrderAndChangesNothing(t *testing.T) {
	f := newUndoFixture(t)
	first, second := f.write("first.txt", "1"), f.write("second.txt", "2")
	f.session(sid1, time.Now(), first, second)
	code, out, _ := runApp(t, "", false, time.Time{}, "undo")
	if code != ExitOK {
		t.Fatalf("code=%d out=%s", code, out)
	}
	if !strings.Contains(out, "restore "+second+" from "+f.quarantineDir()) ||
		strings.Index(out, second) > strings.Index(out, first) ||
		!strings.Contains(out, "dry run: nothing was restored; re-run 'brooom undo "+sid1+" --apply' to restore") {
		t.Fatalf("output:\n%s", out)
	}
	if _, err := os.Lstat(first); err == nil {
		t.Fatal("dry run restored a file")
	}
	if f.reload(sid1).Entries[0].Status != session.StatusApplied {
		t.Fatal("dry run changed the manifest")
	}
}

func TestUndoApplyRestoresParentAfterChildInReverse(t *testing.T) {
	f := newUndoFixture(t)
	child := f.write("dir/sub/child.txt", "child")
	f.write("dir/keep.txt", "keep")
	parent := filepath.Join(f.repo.Dir, "dir")
	// The child is quarantined first, the parent (without it) afterwards.
	f.session(sid1, time.Now(), child, parent)
	code, out, errOut := runApp(t, "", false, time.Time{}, "undo", "--apply", "--yes")
	if code != ExitOK {
		t.Fatalf("code=%d out=%s err=%s", code, out, errOut)
	}
	if fileContent(t, child) != "child" || fileContent(t, filepath.Join(parent, "keep.txt")) != "keep" {
		t.Fatal("files not restored")
	}
	m := f.reload(sid1)
	for _, e := range m.Entries {
		if e.Status != session.StatusRestored {
			t.Fatalf("entry %s is %s", e.Path, e.Status)
		}
	}
	if m.ReclaimedBytes != 0 || !strings.Contains(out, "summary: 2 restored, 0 conflicts, 0 failed, 0 not restorable, 0 already restored") ||
		!strings.Contains(out, "session: "+sid1) {
		t.Fatalf("summary wrong (reclaimed %d):\n%s", m.ReclaimedBytes, out)
	}
	// Running it again is idempotent.
	code, out, _ = runApp(t, "", false, time.Time{}, "undo", "--apply", "--yes", sid1[:12])
	if code != ExitOK || !strings.Contains(out, "already restored") || !strings.Contains(out, "2 already restored") {
		t.Fatalf("second run code=%d:\n%s", code, out)
	}
}

func TestUndoConflictKeepsExistingFileAndExitsOne(t *testing.T) {
	f := newUndoFixture(t)
	a, b := f.write("a.txt", "old a"), f.write("b.txt", "old b")
	f.session(sid1, time.Now(), a, b)
	f.write("a.txt", "new a")
	code, out, errOut := runApp(t, "", false, time.Time{}, "undo", "--apply", "--yes")
	if code != ExitError {
		t.Fatalf("code=%d out=%s err=%s", code, out, errOut)
	}
	if fileContent(t, a) != "new a" || fileContent(t, b) != "old b" {
		t.Fatal("conflicting file was overwritten or the other entry was not restored")
	}
	m := f.reload(sid1)
	if m.Entries[0].Status != session.StatusApplied || m.Entries[1].Status != session.StatusRestored {
		t.Fatalf("statuses %s %s", m.Entries[0].Status, m.Entries[1].Status)
	}
	if !strings.Contains(out, "conflict "+a) || !strings.Contains(out, "1 restored, 1 conflicts") {
		t.Fatalf("output:\n%s", out)
	}
	// The stored copy is still in quarantine.
	if _, err := os.Lstat(m.Entries[0].Trash.StoredPath); err != nil {
		t.Fatalf("stored copy gone: %v", err)
	}
}

func TestUndoDryRunShowsConflictUpFront(t *testing.T) {
	f := newUndoFixture(t)
	a := f.write("a.txt", "old")
	f.session(sid1, time.Now(), a)
	f.write("a.txt", "new")
	code, out, _ := runApp(t, "", false, time.Time{}, "undo")
	if code != ExitOK || !strings.Contains(out, "conflict "+a+": original path already exists") {
		t.Fatalf("code=%d out=%s", code, out)
	}
}

func TestUndoListsNonRestorableEntriesWithHints(t *testing.T) {
	f := newUndoFixture(t)
	good := f.write("good.txt", "g")
	m := f.session(sid1, time.Now(), good)
	lost := f.write("lost.txt", "l")
	rec := m.Entries[0].Trash
	m.Add(session.Entry{Action: findings.ActionTrash, Path: lost, Status: session.StatusApplied, Restorable: true,
		RecoveryHint: "look in the quarantine",
		Trash:        &trash.Record{Strategy: config.StrategyQuarantine, OriginalPath: lost, Restorable: true, StoredPath: filepath.Join(filepath.Dir(rec.StoredPath), "9", "lost.txt")}})
	m.Add(session.Entry{Action: findings.ActionGitGC, Path: f.repo.Dir, Status: session.StatusApplied, RecoveryHint: "gc is permanent"})
	m.Add(session.Entry{Action: findings.ActionTrash, Path: "/x", Status: session.StatusApplied,
		Trash: &trash.Record{Strategy: config.StrategyDelete, OriginalPath: "/x"}})
	m.Add(session.Entry{Action: findings.ActionTrash, Path: "/y", Status: session.StatusFailed, Error: "locked"})
	m.Add(session.Entry{Action: "future-action", Path: f.repo.Dir, Status: session.StatusApplied, Restorable: true})
	if err := f.store.Save(m); err != nil {
		t.Fatal(err)
	}
	code, out, _ := runApp(t, "", false, time.Time{}, "undo", "--apply", "--yes")
	if code != ExitOK {
		t.Fatalf("non-restorable entries must not fail the run: code=%d\n%s", code, out)
	}
	for _, want := range []string{
		"cannot restore " + lost + ": the stored copy", "recovery: look in the quarantine",
		"git maintenance cannot be undone", "recovery: gc is permanent",
		"permanently deleted", "the entry was failed", "unknown action type \"future-action\"",
		"1 restored, 0 conflicts, 0 failed, 5 not restorable",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if fileContent(t, good) != "g" {
		t.Fatal("restorable entry was not restored")
	}
}

func TestUndoConfirmation(t *testing.T) {
	tests := []struct {
		name     string
		stdin    string
		tty      bool
		wantCode int
		restored bool
		errText  string
	}{
		{"declined", "n\n", true, ExitOK, false, ""},
		{"accepted", "y\n", true, ExitOK, true, ""},
		{"non-interactive without --yes", "y\n", false, ExitUsage, false, "pass --yes"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newUndoFixture(t)
			p := f.write("a.txt", "a")
			f.session(sid1, time.Now(), p)
			code, out, errOut := runApp(t, tt.stdin, tt.tty, time.Time{}, "undo", "--apply")
			if code != tt.wantCode || !strings.Contains(errOut, tt.errText) {
				t.Fatalf("code=%d out=%s err=%s", code, out, errOut)
			}
			_, err := os.Lstat(p)
			if restored := err == nil; restored != tt.restored {
				t.Fatalf("restored=%v, want %v", restored, tt.restored)
			}
			if f.reload(sid1).Entries[0].Status == session.StatusRestored != tt.restored {
				t.Fatal("manifest status disagrees with the file system")
			}
			if tt.tty && !strings.Contains(out, "Restore 1 item? [y/N]") {
				t.Fatalf("prompt missing: %s", out)
			}
		})
	}
}

func TestUndoRefusesForgedOutOfScopePath(t *testing.T) {
	f := newUndoFixture(t)
	p := f.write("a.txt", "a")
	m := f.session(sid1, time.Now(), p)
	// A forged manifest asks to restore into a directory outside the repo.
	elsewhere := testutil.ResolvedTempDir(t)
	forged := filepath.Join(elsewhere, "planted.txt")
	m.Entries[0].Trash.OriginalPath = forged
	m.Entries[0].Path = forged
	if err := f.store.Save(m); err != nil {
		t.Fatal(err)
	}
	code, out, _ := runApp(t, "", false, time.Time{}, "undo", "--apply", "--yes")
	if code != ExitOK {
		t.Fatalf("code=%d out=%s", code, out)
	}
	if !strings.Contains(out, "outside the current scope; re-run from ") || !strings.Contains(out, "or with --workspaces") {
		t.Fatalf("scope reason missing:\n%s", out)
	}
	if _, err := os.Lstat(forged); err == nil {
		t.Fatal("restored outside the scope")
	}
	if _, err := os.Lstat(m.Entries[0].Trash.StoredPath); err != nil {
		t.Fatalf("stored copy must stay: %v", err)
	}
}

func TestUndoOutsideRepoIsUsageError(t *testing.T) {
	f := newUndoFixture(t)
	p := f.write("a.txt", "a")
	f.session(sid1, time.Now(), p)
	t.Chdir(testutil.ResolvedTempDir(t))
	code, _, errOut := runApp(t, "", false, time.Time{}, "undo", "--apply", "--yes")
	if code != ExitUsage || !strings.Contains(errOut, "not inside a git repository") {
		t.Fatalf("code=%d err=%q", code, errOut)
	}
	if _, err := os.Lstat(p); err == nil {
		t.Fatal("restored despite the usage error")
	}
}

func TestUndoWithWorkspacesRestoresFromAnywhere(t *testing.T) {
	f := newUndoFixture(t)
	p := f.write("a.txt", "a")
	f.session(sid1, time.Now(), p)
	writeConfig(t, f.home, rootsConfig(filepath.Dir(f.repo.Dir)))
	t.Chdir(testutil.ResolvedTempDir(t))
	code, out, errOut := runApp(t, "", false, time.Time{}, "undo", "--workspaces", "--apply", "--yes")
	if code != ExitOK || fileContent(t, p) != "a" {
		t.Fatalf("code=%d out=%s err=%s", code, out, errOut)
	}
}

func TestUndoUsesRecordedStrategyNotConfig(t *testing.T) {
	f := newUndoFixture(t)
	p := f.write("a.txt", "a")
	f.session(sid1, time.Now(), p)
	// The config now selects another strategy; the entry says quarantine.
	writeConfig(t, f.home, map[string]any{"trash": map[string]any{"strategy": "trash"}})
	code, out, _ := runApp(t, "", false, time.Time{}, "undo", "--apply", "--yes", "--trash-strategy", "trash")
	if code != ExitOK || fileContent(t, p) != "a" {
		t.Fatalf("code=%d out=%s", code, out)
	}
}

func TestUndoMissingStoredCopy(t *testing.T) {
	f := newUndoFixture(t)
	p := f.write("a.txt", "a")
	m := f.session(sid1, time.Now(), p)
	if err := os.RemoveAll(filepath.Dir(m.Entries[0].Trash.StoredPath)); err != nil {
		t.Fatal(err)
	}
	code, out, _ := runApp(t, "", false, time.Time{}, "undo", "--apply", "--yes")
	if code != ExitOK || !strings.Contains(out, "is gone") || !strings.Contains(out, "1 not restorable") {
		t.Fatalf("code=%d out=%s", code, out)
	}
}

func TestUndoInvalidStrategyFlagIsUsageError(t *testing.T) {
	newUndoFixture(t)
	code, _, _ := runApp(t, "", false, time.Time{}, "undo", "--trash-strategy", "bogus")
	if code != ExitUsage {
		t.Fatalf("code=%d", code)
	}
}
