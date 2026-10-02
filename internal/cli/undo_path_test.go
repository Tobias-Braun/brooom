package cli

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/session"
	"github.com/Tobias-Braun/brooom/internal/testutil"
)

// pathSession runs `sweep tidy <repo>` from outside the repository's working
// directory (test trash, isolated home) and returns the removed file, the
// arguments of the printed undo command and the fixture.
func pathSession(t *testing.T) (removed string, undoArgs []string, f *cleanupFixture) {
	t.Helper()
	f = newCleanupFixture(t, nil)
	file := oldJunk(t, f.repo.Dir)
	t.Chdir(testutil.ResolvedTempDir(t))
	code, out, errOut := brooom(t, "", "sweep", "tidy", f.repo.Dir, "--yes")
	if code != ExitOK || exists(file) {
		t.Fatalf("apply: code %d, file kept %v\n%s\n%s", code, exists(file), out, errOut)
	}
	for _, line := range strings.Split(out, "\n") {
		if rest, ok := strings.CutPrefix(line, "undo: brooom "); ok {
			undoArgs = strings.Fields(rest)
		}
	}
	if len(undoArgs) == 0 {
		t.Fatalf("no undo hint in output:\n%s", out)
	}
	return file, undoArgs, f
}

// TestUndoHintReproducesPathScope runs exactly the printed undo command from a
// directory outside any repository (#192).
func TestUndoHintReproducesPathScope(t *testing.T) {
	removed, undoArgs, f := pathSession(t)
	if undoArgs[0] != "undo" || undoArgs[1] != f.sessions()[0].ID || !slices.Contains(undoArgs, "--path") {
		t.Fatalf("undo hint %v lacks the id or --path", undoArgs)
	}
	t.Chdir(testutil.ResolvedTempDir(t))
	code, out, errOut := brooom(t, "", append(undoArgs, "--yes")...)
	if code != ExitOK || !exists(removed) {
		t.Fatalf("the printed undo command did not restore: code %d\n%s\n%s", code, out, errOut)
	}
}

// TestUndoStaysInsideThePath keeps the safety check in place: --path only
// widens the scope to that folder, so a forged entry elsewhere is refused.
func TestUndoStaysInsideThePath(t *testing.T) {
	_, _, f := pathSession(t)
	m := f.sessions()[0]
	forged := testutil.ResolvedTempDir(t) + "/planted"
	m.Entries[0].Trash.OriginalPath = forged
	m.Entries[0].Path = forged
	if err := session.NewStore(filepath.Join(f.home, "sessions")).Save(m); err != nil {
		t.Fatal(err)
	}
	t.Chdir(testutil.ResolvedTempDir(t))
	code, out, _ := brooom(t, "", "undo", "--path", f.repo.Dir, "--yes")
	if code != ExitOK || exists(forged) || !strings.Contains(out, "1 skipped (outside scope") {
		t.Fatalf("code %d, forged path restored %v\n%s", code, exists(forged), out)
	}
}

// TestUndoOfOldWorkspaceSessionExplainsPath: a session of an earlier release
// recorded --workspaces; undo can no longer derive that scope and says how to
// pass it.
func TestUndoOfOldWorkspaceSessionExplainsPath(t *testing.T) {
	f := newUndoFixture(t)
	p := f.write("a.txt", "a")
	m := f.session(sid1, testutil.BaseTime, p)
	m.Workspaces = true
	if err := f.store.Save(m); err != nil {
		t.Fatal(err)
	}
	code, _, errOut := runApp(t, "", false, testutil.BaseTime, "undo", "--dry-run")
	if code != ExitOK || !strings.Contains(errOut, "pass --path <folder>") {
		t.Fatalf("code %d, stderr %q", code, errOut)
	}
}

// TestUndoScopeRefusalIsNotCountedAsNotRestorable is the wording of #192: data
// that sits safely in the trash is skipped, never "not restorable".
func TestUndoScopeRefusalIsNotCountedAsNotRestorable(t *testing.T) {
	f := newUndoFixture(t)
	p := f.write("a.txt", "a")
	m := f.session(sid1, testutil.BaseTime, p)
	elsewhere := testutil.ResolvedTempDir(t) + "/planted.txt"
	m.Entries[0].Trash.OriginalPath = elsewhere
	m.Entries[0].Path = elsewhere
	if err := f.store.Save(m); err != nil {
		t.Fatal(err)
	}
	code, out, _ := runApp(t, "", false, testutil.BaseTime, "undo", "--yes")
	want := "summary: 0 restored, 0 conflicts, 0 failed, 0 not restorable, 0 already restored, 1 skipped (outside scope; re-run with --path)"
	if code != ExitOK || !strings.Contains(out, want) || strings.Contains(out, "cannot restore") {
		t.Fatalf("code %d, want summary %q:\n%s", code, want, out)
	}
}

// TestUndoHintKeepsPathAndConfigFlags checks every scope flag of the original
// invocation, quoted for the host shell.
func TestUndoHintKeepsPathAndConfigFlags(t *testing.T) {
	a := &app{goos: "linux"}
	a.flags.path = "/ws/a b"
	a.flags.configPath = "/cfg/brooom.json"
	got := strings.Join(a.scopeFlags(), " ")
	want := "--config /cfg/brooom.json --path '/ws/a b'"
	if got != want {
		t.Fatalf("scope flags %q, want %q", got, want)
	}
}

// TestUndoHintOmitsScopeForRepoSessions guards the plain case: without scope
// flags the hint stays `brooom undo <id>`.
func TestUndoHintOmitsScopeForRepoSessions(t *testing.T) {
	f := newCleanupFixture(t, nil)
	file := oldJunk(t, f.repo.Dir)
	code, out, _ := brooom(t, "", "sweep", "tidy", "--yes")
	if code != ExitOK || exists(file) {
		t.Fatalf("code %d\n%s", code, out)
	}
	ms := f.sessions()
	if !strings.Contains(out, "undo: brooom undo "+ms[0].ID+"\n") {
		t.Fatalf("hint:\n%s", out)
	}
}
