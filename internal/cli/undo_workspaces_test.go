package cli

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/session"
	"github.com/Tobias-Braun/brooom/internal/testutil"
)

// workspaceSession applies `clean --from -w` on a repository below a
// configured root (quarantine strategy, isolated home) and returns the removed
// file, the arguments of the printed undo command and the fixture.
func workspaceSession(t *testing.T) (removed string, undoArgs []string, f *cleanupFixture) {
	t.Helper()
	f = newCleanupFixture(t, nil)
	cfg := rootsConfig(f.repo.Dir)
	cfg["git"] = map[string]any{"use_gh": false}
	writeConfig(t, f.home, cfg)
	dir, file := junkDir(t, f.repo.Dir, "node_modules")
	path := writeReportFile(t, trashFinding(f.repo.Dir, dir))
	code, out, errOut := clean(t, "", "--from", path, "-w", "--yes")
	if code != ExitOK || exists(file) {
		t.Fatalf("workspace apply: code %d, file kept %v\n%s\n%s", code, exists(file), out, errOut)
	}
	for _, line := range strings.Split(out, "\n") {
		if rest, ok := strings.CutPrefix(line, "undo: brooom "); ok {
			undoArgs = strings.Fields(rest)
		}
	}
	if len(undoArgs) == 0 {
		t.Fatalf("no undo hint in output:\n%s", out)
	}
	ms := f.sessions()
	if len(ms) != 1 || !ms[0].Workspaces {
		t.Fatalf("the manifest must record the workspaces scope: %+v", ms)
	}
	return file, undoArgs, f
}

// TestUndoHintReproducesWorkspaceScope runs exactly the printed undo command
// from a directory outside any repository (#192).
func TestUndoHintReproducesWorkspaceScope(t *testing.T) {
	removed, undoArgs, f := workspaceSession(t)
	if undoArgs[0] != "undo" || undoArgs[1] != f.sessions()[0].ID || !slices.Contains(undoArgs, "--workspaces") {
		t.Fatalf("undo hint %v lacks the id or --workspaces", undoArgs)
	}
	t.Chdir(testutil.ResolvedTempDir(t))
	code, out, errOut := brooom(t, "", append(undoArgs, "--yes")...)
	if code != ExitOK || !exists(removed) {
		t.Fatalf("the printed undo command did not restore: code %d\n%s\n%s", code, out, errOut)
	}
}

// TestUndoOfWorkspaceSessionNeedsNoFlag covers the manifest-derived scope: a
// plain `brooom undo`, with or without the id, works from anywhere for a
// session applied with --workspaces.
func TestUndoOfWorkspaceSessionNeedsNoFlag(t *testing.T) {
	for _, withID := range []bool{false, true} {
		removed, _, f := workspaceSession(t)
		t.Chdir(testutil.ResolvedTempDir(t))
		args := []string{"undo", "--yes"}
		if withID {
			args = []string{"undo", f.sessions()[0].ID, "--yes"}
		}
		code, out, errOut := brooom(t, "", args...)
		if code != ExitOK || !exists(removed) || !strings.Contains(out, "1 restored") {
			t.Fatalf("%v: code %d\n%s\n%s", args, code, out, errOut)
		}
	}
}

// TestUndoOfWorkspaceSessionStaysInsideConfiguredRoots keeps the safety check
// in place: the recorded workspaces flag widens the scope to the configured
// roots only, so a forged entry elsewhere is still refused.
func TestUndoOfWorkspaceSessionStaysInsideConfiguredRoots(t *testing.T) {
	_, _, f := workspaceSession(t)
	m := f.sessions()[0]
	forged := testutil.ResolvedTempDir(t) + "/planted"
	m.Entries[0].Trash.OriginalPath = forged
	m.Entries[0].Path = forged
	if err := session.NewStore(filepath.Join(f.home, "sessions")).Save(m); err != nil {
		t.Fatal(err)
	}
	t.Chdir(testutil.ResolvedTempDir(t))
	code, out, _ := brooom(t, "", "undo", "--yes")
	if code != ExitOK || exists(forged) || !strings.Contains(out, "1 skipped (outside scope") {
		t.Fatalf("code %d, forged path restored %v\n%s", code, exists(forged), out)
	}
}

// TestUndoScopeRefusalIsNotCountedAsNotRestorable is the wording of #192: data
// that sits safely in quarantine is skipped, never "not restorable".
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
	want := "summary: 0 restored, 0 conflicts, 0 failed, 0 not restorable, 0 already restored, 1 skipped (outside scope; re-run with -w)"
	if code != ExitOK || !strings.Contains(out, want) || strings.Contains(out, "cannot restore") {
		t.Fatalf("code %d, want summary %q:\n%s", code, want, out)
	}
}

// TestUndoHintKeepsRootAndConfigFlags checks every scope flag of the original
// invocation, quoted for the host shell.
func TestUndoHintKeepsRootAndConfigFlags(t *testing.T) {
	a := &app{goos: "linux"}
	a.flags.workspaces = true
	a.flags.roots = []string{"/ws/a b"}
	a.flags.configPath = "/cfg/brooom.json"
	got := strings.Join(a.scopeFlags(), " ")
	want := "--config /cfg/brooom.json --workspaces --root '/ws/a b'"
	if got != want {
		t.Fatalf("scope flags %q, want %q", got, want)
	}
}

// TestUndoHintOmitsScopeForRepoSessions guards the plain case: without scope
// flags the hint stays `brooom undo <id>`.
func TestUndoHintOmitsScopeForRepoSessions(t *testing.T) {
	f := newCleanupFixture(t, nil)
	dir, file := junkDir(t, f.repo.Dir, "node_modules")
	code, out, _ := clean(t, "", "--from", writeReportFile(t, trashFinding(f.repo.Dir, dir)), "--yes")
	if code != ExitOK || exists(file) {
		t.Fatalf("code %d\n%s", code, out)
	}
	ms := f.sessions()
	if ms[0].Workspaces || !strings.Contains(out, "undo: brooom undo "+ms[0].ID+"\n") {
		t.Fatalf("workspaces=%v\n%s", ms[0].Workspaces, out)
	}
}
