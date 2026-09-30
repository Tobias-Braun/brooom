package trash

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// removeN quarantines count small files named f000, f001, ... (equal-length
// names keep every journal line about the same size) and returns their records.
func removeN(t *testing.T, q *quarantine, root string, from, count int) []Record {
	t.Helper()
	var recs []Record
	for i := from; i < from+count; i++ {
		p := filepath.Join(root, "work", fmt.Sprintf("f%03d", i))
		writeFile(t, p, "x", 0o644)
		rec, err := q.Remove(context.Background(), p)
		if err != nil {
			t.Fatal(err)
		}
		recs = append(recs, rec)
	}
	return recs
}

func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Size()
}

// TestQuarantineRemoveWritesLinearBytes is the regression test for the
// quadratic manifest rewrite: manifest.json is written once and every further
// item costs one journal line of constant size.
func TestQuarantineRemoveWritesLinearBytes(t *testing.T) {
	q, root := newTestQuarantine(t, "sess")
	sd := filepath.Join(q.dir, "sess")
	manifest := filepath.Join(sd, ManifestName)
	journal := filepath.Join(sd, JournalName)

	removeN(t, q, root, 0, 1)
	base, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	s1 := fileSize(t, journal)
	removeN(t, q, root, 1, 10)
	s11 := fileSize(t, journal)
	removeN(t, q, root, 11, 90)
	s101 := fileSize(t, journal)
	removeN(t, q, root, 101, 10)
	s111 := fileSize(t, journal)

	after, _ := os.ReadFile(manifest)
	if string(after) != string(base) {
		t.Errorf("manifest.json was rewritten during Remove:\n%s", after)
	}
	first, last := s11-s1, s111-s101
	if first <= 0 || last > first*11/10 || last < first*9/10 {
		t.Errorf("bytes per 10 items grew: first %d, last %d", first, last)
	}
	if got := len(loadManifest(t, q).Items); got != 111 {
		t.Errorf("items = %d, want 111", got)
	}
}

func TestQuarantineJournalVisibleToOtherInstanceAndListing(t *testing.T) {
	const sid = "20260501-120000-abcd"
	q, root := newTestQuarantine(t, sid)
	removeN(t, q, root, 0, 3)

	// A fresh trasher (a later process) continues numbering from the journal.
	q2, err := newQuarantine(Options{SessionID: sid, QuarantineDir: q.dir})
	if err != nil {
		t.Fatal(err)
	}
	recs := removeN(t, q2.(*quarantine), root, 3, 1)
	if !strings.HasSuffix(filepath.ToSlash(recs[0].StoredPath), "/"+sid+"/4/f003") {
		t.Errorf("stored = %q, want item 4", recs[0].StoredPath)
	}

	l, err := ListQuarantine(q.dir, fixedNow.AddDate(0, 0, 60), 30)
	if err != nil {
		t.Fatal(err)
	}
	var want int64
	for _, it := range loadManifest(t, q).Items {
		want += it.SizeBytes
	}
	if len(l.Expired) != 1 || !l.Expired[0].FromManifest || l.Expired[0].SizeBytes != want || want == 0 {
		t.Errorf("listing = %+v, want one manifest session of %d bytes (journal included)", l.Expired, want)
	}
}

func TestQuarantineRestoreFoldsJournalAndDropsCache(t *testing.T) {
	q, root := newTestQuarantine(t, "sess")
	recs := removeN(t, q, root, 0, 3)
	if err := q.Restore(context.Background(), recs[1]); err != nil {
		t.Fatal(err)
	}
	sd := filepath.Join(q.dir, "sess")
	if _, err := os.Stat(filepath.Join(sd, JournalName)); !os.IsNotExist(err) {
		t.Errorf("journal still present after the manifest was rewritten: %v", err)
	}
	m := loadManifest(t, q)
	if len(m.Items) != 2 || m.Items[0].N != 1 || m.Items[1].N != 3 {
		t.Fatalf("items = %+v, want counters 1 and 3", m.Items)
	}
	// The cache must not resurrect the restored item on the next Remove.
	removeN(t, q, root, 3, 1)
	m = loadManifest(t, q)
	if len(m.Items) != 3 || m.Items[2].N != 4 {
		t.Errorf("items after Remove = %+v, want counters 1, 3, 4", m.Items)
	}
}

func TestQuarantineJournalReplay(t *testing.T) {
	tests := []struct {
		name    string
		journal string
		want    int
		wantErr bool
	}{
		{"torn tail ignored", `{"n":2,"original_path":"/a"}` + "\n" + `{"n":3,"orig`, 2, false},
		{"already in manifest skipped", `{"n":1,"original_path":"/a"}` + "\n", 1, false},
		{"corrupt middle line", "{bad\n" + `{"n":2}` + "\n", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sd := t.TempDir()
			writeFile(t, filepath.Join(sd, ManifestName), `{"version":1,"session_id":"s","created_at":"2026-05-01T12:00:00Z","items":[{"n":1,"original_path":"/a"}]}`, 0o600)
			writeFile(t, filepath.Join(sd, JournalName), tt.journal, 0o600)
			m, err := loadQuarantineManifest(sd)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if err == nil && len(m.Items) != tt.want {
				t.Errorf("items = %+v, want %d", m.Items, tt.want)
			}
		})
	}
}
