package session

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/trash"
)

func TestMarkPurged(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "sessions"))
	q := filepath.Join(t.TempDir(), "quarantine")
	purged := filepath.Join(q, "20260101-000000-aaaa")
	kept := filepath.Join(q, "20260301-000000-bbbb")
	rec := func(dir string, s config.TrashStrategy) *trash.Record {
		return &trash.Record{Strategy: s, StoredPath: filepath.Join(dir, "1", "f"), Restorable: true}
	}
	m := &Manifest{ID: "20260101-000000-aaaa", StartedAt: time.Now()}
	m.Add(Entry{Action: "trash", Path: "/a", Status: StatusApplied, Restorable: true, SizeBytes: 5, Trash: rec(purged, config.StrategyQuarantine)})
	m.Add(Entry{Action: "trash", Path: "/b", Status: StatusApplied, Restorable: true, SizeBytes: 5, Trash: rec(kept, config.StrategyQuarantine)})
	m.Add(Entry{Action: "trash", Path: "/c", Status: StatusApplied, Restorable: true, SizeBytes: 5, Trash: rec(purged, config.StrategyTrash)})
	m.Add(Entry{Action: "trash", Path: "/d", Status: StatusRestored, Restorable: true, Trash: rec(purged, config.StrategyQuarantine)})
	m.Add(Entry{Action: "delete-branch", Path: "/e", Status: StatusApplied, Restorable: true})
	if err := store.Save(m); err != nil {
		t.Fatal(err)
	}
	other := &Manifest{ID: "20260201-000000-cccc", StartedAt: time.Now()}
	other.Add(Entry{Action: "trash", Path: "/f", Status: StatusApplied, Restorable: true, Trash: rec(kept, config.StrategyQuarantine)})
	if err := store.Save(other); err != nil {
		t.Fatal(err)
	}

	at := time.Date(2026, 9, 30, 8, 0, 0, 0, time.UTC)
	n, err := store.MarkPurged([]string{purged}, at)
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	got, err := store.Load(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	e := got.Entries[0]
	if e.Restorable || e.Trash.Restorable || e.RecoveryHint != "purged from quarantine on 2026-09-30" {
		t.Fatalf("entry not marked: %+v", e)
	}
	for i := 1; i < 5; i++ {
		if !got.Entries[i].Restorable {
			t.Errorf("entry %d must be untouched: %+v", i, got.Entries[i])
		}
	}
	if o, _ := store.Load(other.ID); !o.Entries[0].Restorable {
		t.Fatal("unrelated session changed")
	}
	if n, err := store.MarkPurged(nil, at); n != 0 || err != nil {
		t.Fatalf("no dirs: %d %v", n, err)
	}
}
