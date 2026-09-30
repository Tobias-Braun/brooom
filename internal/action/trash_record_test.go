package action

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/session"
	"github.com/Tobias-Braun/brooom/internal/trash"
)

func TestCheckRecordPaths(t *testing.T) {
	root := t.TempDir()
	orig := filepath.Join(root, "repo", "a.txt")
	stored := filepath.Join(root, "trash", "files", "a.txt")
	info := filepath.Join(root, "trash", "info", "a.txt.trashinfo")
	base := trash.Record{OriginalPath: orig, StoredPath: stored, InfoPath: info}
	with := func(mod func(*trash.Record)) trash.Record { r := base; mod(&r); return r }

	tests := []struct {
		name    string
		rec     trash.Record
		wantErr bool
	}{
		{"ordinary record", base, false},
		{"no stored path", with(func(r *trash.Record) { r.StoredPath = ""; r.InfoPath = "" }), false},
		{"no info path", with(func(r *trash.Record) { r.InfoPath = "" }), false},
		{"relative stored", with(func(r *trash.Record) { r.StoredPath = "trash/files/a.txt" }), true},
		{"unclean stored", with(func(r *trash.Record) { r.StoredPath = root + "/trash/files/../files/a.txt" }), true},
		{"relative info", with(func(r *trash.Record) { r.InfoPath = "info/a.trashinfo" }), true},
		{"unclean info", with(func(r *trash.Record) { r.InfoPath = root + "/trash/info/../info/a.trashinfo" }), true},
		{"stored inside .git", with(func(r *trash.Record) { r.StoredPath = filepath.Join(root, "repo", ".git", "HEAD") }), true},
		{"info inside .git", with(func(r *trash.Record) { r.InfoPath = filepath.Join(root, "repo", ".git", "x") }), true},
		{"stored equals destination", with(func(r *trash.Record) { r.StoredPath = orig }), true},
		{"stored inside destination", with(func(r *trash.Record) { r.StoredPath = filepath.Join(orig, "x") }), true},
		{"destination inside stored", with(func(r *trash.Record) { r.OriginalPath = filepath.Join(stored, "x") }), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := checkRecordPaths(tt.rec); (err != nil) != tt.wantErr {
				t.Errorf("err = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// TestTrashUndoRefusesForgedRecordPaths checks that the action layer stops a
// forged StoredPath or InfoPath before any trasher is asked to restore,
// whichever strategy the entry names.
func TestTrashUndoRefusesForgedRecordPaths(t *testing.T) {
	fx := newTrashFixture(t)
	stub := &stubTrasher{strategy: config.StrategyTrash}
	fx.useStub(stub)
	fx.mkdir("proj")
	dest := fx.path("proj/stolen.txt")

	tests := []struct {
		name string
		rec  trash.Record
	}{
		{"relative stored path", trash.Record{StoredPath: "files/secret.txt"}},
		{"traversing stored path", trash.Record{StoredPath: fx.path("t") + "/files/../../secret.txt"}},
		{"relative info path", trash.Record{StoredPath: fx.path("t/files/a"), InfoPath: "a.trashinfo"}},
		{"stored path inside git metadata", trash.Record{StoredPath: fx.path("proj/.git/config")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := tt.rec
			rec.Strategy, rec.OriginalPath, rec.Restorable = config.StrategyTrash, dest, true
			e := session.Entry{Status: session.StatusApplied, Path: dest, Trash: &rec}
			err := trashAction{}.Undo(context.Background(), fx.env, e)
			if err == nil || !strings.Contains(err.Error(), "refusing to restore") {
				t.Fatalf("Undo err = %v, want a refusal", err)
			}
			if len(stub.restored) != 0 {
				t.Fatal("trasher was called")
			}
		})
	}
}

func TestRecordTrasherRefusesForgedRecordPaths(t *testing.T) {
	fx := newTrashFixture(t)
	stub := &stubTrasher{strategy: config.StrategyTrash}
	fx.useStub(stub)
	path := fx.path("wt")
	rec := trash.Record{Strategy: config.StrategyTrash, OriginalPath: path, StoredPath: "relative/files/wt"}
	if _, err := recordTrasher(fx.env, rec, path); err == nil || !strings.Contains(err.Error(), "refusing to restore") {
		t.Fatalf("err = %v, want a refusal", err)
	}
	rec.StoredPath = fx.path("t/files/wt")
	rec.InfoPath = "info/wt.trashinfo"
	if _, err := recordTrasher(fx.env, rec, path); err == nil {
		t.Fatal("a relative info path was accepted")
	}
}
