package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/session"
	"github.com/Tobias-Braun/brooom/internal/trash"
	"github.com/Tobias-Braun/brooom/internal/walk"
)

// trashFixture fakes what a sweep with the OS trash strategy leaves behind:
// two files in a temporary ".Trash" folder recorded in one session manifest,
// and a file of the user's own in the same trash that brooom never moved.
// The real OS trash is never touched.
type trashFixture struct {
	store          *session.Store
	id             string
	stored, theirs string
	recs           []trash.Record
}

func newTrashFixture(t *testing.T) *trashFixture {
	t.Helper()
	home := isolate(t)
	trashDir := filepath.Join(t.TempDir(), ".Trash")
	if err := os.MkdirAll(trashDir, 0o755); err != nil {
		t.Fatal(err)
	}
	f := &trashFixture{store: session.NewStore(filepath.Join(home, "sessions")), id: "20260930-120000-abcd"}
	m := &session.Manifest{Version: session.ManifestVersion, ID: f.id, StartedAt: time.Now().UTC(), Command: "brooom sweep --yes"}
	for i, name := range []string{"app.log", "debug.log"} {
		p := filepath.Join(trashDir, name)
		if err := os.WriteFile(p, []byte(strings.Repeat("x", 100*(i+1))), 0o600); err != nil {
			t.Fatal(err)
		}
		fi, err := os.Lstat(p)
		if err != nil {
			t.Fatal(err)
		}
		rec := trash.Record{Strategy: config.StrategyTrash, OriginalPath: "/code/" + name, StoredPath: p, SizeBytes: walk.AllocatedSize(fi), Restorable: true}
		f.recs = append(f.recs, rec)
		m.Add(session.Entry{Detector: "log-and-runtime-files", Action: findings.ActionTrash, Path: rec.OriginalPath, SizeBytes: rec.SizeBytes, Status: session.StatusApplied, Trash: &rec, Restorable: true})
	}
	f.stored = f.recs[0].StoredPath
	f.theirs = filepath.Join(trashDir, "photo.jpg")
	if err := os.WriteFile(f.theirs, []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Save(m); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestEmptyTrashDeletesOnlyBrooomsItems(t *testing.T) {
	f := newTrashFixture(t)
	code, out, errOut := runApp(t, "y\n", true, time.Time{}, "empty-trash")
	if code != ExitOK {
		t.Fatalf("code %d, stderr %q\n%s", code, errOut, out)
	}
	for _, want := range []string{"/code/app.log", "/code/debug.log", "session " + f.id, "total: 2 items", "Permanently delete 2 items", "deleted 2 items from the trash", "marked 2 session entries as not restorable"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	for _, r := range f.recs {
		if exists(r.StoredPath) {
			t.Errorf("%s is still in the trash", r.StoredPath)
		}
	}
	if !exists(f.theirs) {
		t.Fatal("a file brooom never moved was deleted from the trash")
	}
	m, err := f.store.Load(f.id)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range m.Entries {
		if e.Restorable || e.Trash.Restorable || !strings.Contains(e.RecoveryHint, "deleted from the trash") {
			t.Errorf("entry %s not marked: %+v", e.Path, e)
		}
	}
	// A second run finds nothing left.
	if _, out, _ := runApp(t, "", false, time.Time{}, "empty-trash"); !strings.Contains(out, "nothing in the trash from brooom") {
		t.Errorf("second run:\n%s", out)
	}
}

func TestEmptyTrashDeclineAndDryRunChangeNothing(t *testing.T) {
	for _, tc := range []struct {
		stdin string
		args  []string
		want  string
	}{{"n\n", nil, "nothing was deleted"}, {"", []string{"--dry-run"}, "dry run: nothing was deleted"}} {
		f := newTrashFixture(t)
		code, out, _ := runApp(t, tc.stdin, true, time.Time{}, append([]string{"empty-trash"}, tc.args...)...)
		if code != ExitOK || !strings.Contains(out, tc.want) || !exists(f.stored) {
			t.Errorf("%v: code %d, stored kept %v\n%s", tc.args, code, exists(f.stored), out)
		}
	}
}

func TestEmptyTrashNeedsConfirmation(t *testing.T) {
	f := newTrashFixture(t)
	code, _, errOut := runApp(t, "", false, time.Time{}, "empty-trash")
	if code != ExitUsage || !exists(f.stored) {
		t.Errorf("code %d, stderr %q", code, errOut)
	}
	if code, _, _ := runApp(t, "", false, time.Time{}, "empty-trash", "--yes"); code != ExitOK || exists(f.stored) {
		t.Errorf("--yes: code %d, stored kept %v", code, exists(f.stored))
	}
}

// TestEmptyTrashKeepsChangedItems: a stored file whose size no longer matches
// the record is not what brooom put there, so it is listed and kept.
func TestEmptyTrashKeepsChangedItems(t *testing.T) {
	f := newTrashFixture(t)
	if err := os.WriteFile(f.stored, []byte(strings.Repeat("y", 50000)), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := runApp(t, "", false, time.Time{}, "empty-trash", "--yes")
	if code != ExitOK {
		t.Fatalf("code %d, stderr %q\n%s", code, errOut, out)
	}
	if !exists(f.stored) || !strings.Contains(out, "kept:") || !strings.Contains(out, "changed since brooom moved it") {
		t.Errorf("a changed item was not kept:\n%s", out)
	}
	if exists(f.recs[1].StoredPath) {
		t.Error("the unchanged item was not deleted")
	}
}
