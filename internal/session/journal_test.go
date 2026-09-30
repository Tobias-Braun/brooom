package session

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// appendN adds count entries to m, journaling each one like the executor.
func appendN(t *testing.T, s *Store, m *Manifest, count int) {
	t.Helper()
	for i := 0; i < count; i++ {
		e := Entry{Path: fmt.Sprintf("/work/f%04d", len(m.Entries)), SizeBytes: 10, Status: StatusApplied, At: t0}
		m.Add(e)
		if err := s.AppendEntry(m.ID, len(m.Entries)-1, e); err != nil {
			t.Fatal(err)
		}
	}
}

func size(t *testing.T, path string) int64 {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Size()
}

// TestAppendEntryWritesLinearBytes is the regression test for the quadratic
// manifest rewrite: the snapshot is not touched per entry and the journal
// grows by a constant number of bytes per entry.
func TestAppendEntryWritesLinearBytes(t *testing.T) {
	s := newTestStore(t)
	m := saveAt(t, s, "lin", t0)
	snapshot := filepath.Join(s.Dir, "lin.json")
	journal := filepath.Join(s.Dir, "lin.journal")
	before, err := os.ReadFile(snapshot)
	if err != nil {
		t.Fatal(err)
	}

	appendN(t, s, m, 10)
	s10 := size(t, journal)
	appendN(t, s, m, 480)
	s490 := size(t, journal)
	appendN(t, s, m, 10)
	s500 := size(t, journal)

	after, _ := os.ReadFile(snapshot)
	if string(after) != string(before) {
		t.Error("manifest snapshot was rewritten while entries were appended")
	}
	if first, last := s10, s500-s490; last > first*11/10 || last < first*9/10 {
		t.Errorf("bytes per 10 entries grew: first %d, last %d", first, last)
	}
}

func TestLoadReplaysJournal(t *testing.T) {
	s := newTestStore(t)
	m := saveAt(t, s, "j1", t0)
	appendN(t, s, m, 3)

	got, err := s.Load("j1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Entries) != 3 || got.ReclaimedBytes != 30 {
		t.Fatalf("Load = %d entries, %d bytes; want 3, 30", len(got.Entries), got.ReclaimedBytes)
	}
	list, problems, err := s.List()
	if err != nil || len(problems) != 0 || len(list) != 1 || len(list[0].Entries) != 3 {
		t.Fatalf("List = %v, %v, %v", list, problems, err)
	}
}

func TestSaveCompactsJournal(t *testing.T) {
	s := newTestStore(t)
	m := saveAt(t, s, "j2", t0)
	appendN(t, s, m, 2)
	m.Finish(t0.Add(time.Minute))
	if err := s.Save(m); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(s.Dir, "j2.journal")); !os.IsNotExist(err) {
		t.Errorf("journal survived Save: %v", err)
	}
	got, err := s.Load("j2")
	if err != nil || len(got.Entries) != 2 || got.FinishedAt.IsZero() {
		t.Fatalf("Load = %+v, %v", got, err)
	}
}

func TestJournalReplay(t *testing.T) {
	line := func(i int) string {
		return fmt.Sprintf(`{"i":%d,"entry":{"path":"/p%d","size_bytes":5,"status":"applied"}}`, i, i) + "\n"
	}
	tests := []struct {
		name    string
		journal string
		want    int
		wantErr bool
	}{
		{"torn tail ignored", line(0) + `{"i":1,"entr`, 1, false},
		{"entries already in snapshot skipped", line(0) + line(1) + line(0), 2, false},
		{"corrupt middle line", line(0) + "{bad\n" + line(1), 0, true},
		{"gap", line(0) + line(2), 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestStore(t)
			saveAt(t, s, "r1", t0)
			writeRaw(t, s, "r1.journal", tt.journal)
			m, err := s.Load("r1")
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if err == nil && len(m.Entries) != tt.want {
				t.Errorf("entries = %d, want %d", len(m.Entries), tt.want)
			}
		})
	}
}

func TestAppendEntryRejectsBadID(t *testing.T) {
	s := newTestStore(t)
	if err := s.AppendEntry("../x", 0, Entry{}); err == nil {
		t.Error("expected an error for an id with a separator")
	}
}

// idMismatchStore returns a store holding X.json (one entry) and a copy
// X.backup.json whose id is still X.
func idMismatchStore(t *testing.T) *Store {
	t.Helper()
	s := newTestStore(t)
	m := saveAt(t, s, "X", t0)
	m.Add(Entry{Path: "/a", Status: StatusApplied})
	if err := s.Save(m); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(s.Dir, "X.json"))
	if err != nil {
		t.Fatal(err)
	}
	writeRaw(t, s, "X.backup.json", string(data))
	return s
}

// TestLoadRejectsIDDifferingFromFileName is the regression test for #223: a
// copy such as X.backup.json whose id is X must not load as X, or undo would
// save it over the real X.json.
func TestLoadRejectsIDDifferingFromFileName(t *testing.T) {
	s := idMismatchStore(t)
	_, err := s.Load("X.backup")
	if err == nil || !strings.Contains(err.Error(), "does not match its file name") {
		t.Errorf("Load(X.backup) err = %v, want id mismatch", err)
	}
	if errors.Is(err, ErrNotFound) {
		t.Error("mismatch reported as not found")
	}
	if got, err := s.Load("X"); err != nil || len(got.Entries) != 1 {
		t.Errorf("Load(X) = %+v, %v", got, err)
	}
}

func TestListReportsIDDifferingFromFileNameAsProblem(t *testing.T) {
	list, problems, err := idMismatchStore(t).List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != "X" {
		t.Errorf("List = %v, want only X", list)
	}
	if len(problems) != 1 || !strings.HasSuffix(problems[0].File, "X.backup.json") {
		t.Errorf("problems = %+v, want X.backup.json", problems)
	}
}
