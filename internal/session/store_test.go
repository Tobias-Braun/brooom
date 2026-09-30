package session

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 9, 29, 22, 45, 1, 0, time.UTC)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	return NewStore(filepath.Join(t.TempDir(), "sessions"))
}

func saveAt(t *testing.T, s *Store, id string, started time.Time) *Manifest {
	t.Helper()
	m := &Manifest{ID: id, StartedAt: started, Command: "sweep"}
	if err := s.Save(m); err != nil {
		t.Fatalf("Save(%s): %v", id, err)
	}
	return m
}

func TestSaveLoadRoundTrip(t *testing.T) {
	s := newTestStore(t)
	m := NewManifest(t0, "sweep --apply")
	m.Add(Entry{Path: "/a", SizeBytes: 10, Status: StatusApplied, Restorable: true, At: t0})
	m.Finish(t0.Add(time.Minute))
	if err := s.Save(m); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != m.ID || got.Command != m.Command || len(got.Entries) != 1 ||
		got.ReclaimedBytes != 10 || !got.FinishedAt.Equal(m.FinishedAt) || got.Version != ManifestVersion {
		t.Fatalf("round trip mismatch: %+v", got)
	}
}

func TestSaveSetsVersion(t *testing.T) {
	s := newTestStore(t)
	m := &Manifest{ID: "x1"}
	if err := s.Save(m); err != nil {
		t.Fatal(err)
	}
	if m.Version != ManifestVersion {
		t.Fatalf("version = %d", m.Version)
	}
}

func TestPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits are not meaningful on Windows")
	}
	s := newTestStore(t)
	saveAt(t, s, "p1", t0)
	di, err := os.Stat(s.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if di.Mode().Perm() != 0o700 {
		t.Errorf("dir mode = %v", di.Mode().Perm())
	}
	fi, err := os.Stat(filepath.Join(s.Dir, "p1.json"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("file mode = %v", fi.Mode().Perm())
	}
}

func TestInvalidIDs(t *testing.T) {
	s := newTestStore(t)
	for _, id := range []string{"", "../x", "a/b", `a\b`, "a..b", "a\x00b", ".."} {
		if err := s.Save(&Manifest{ID: id}); err == nil {
			t.Errorf("Save(%q) succeeded", id)
		}
		if _, err := s.Load(id); err == nil || errors.Is(err, ErrNotFound) {
			t.Errorf("Load(%q) = %v, want validation error", id, err)
		}
	}
}

func TestSaveFailureLeavesNoPartialFile(t *testing.T) {
	s := newTestStore(t)
	// A non-empty directory at the target path makes the final rename fail
	// on every platform.
	blocker := filepath.Join(s.Dir, "b1.json")
	if err := os.MkdirAll(filepath.Join(blocker, "keep"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(&Manifest{ID: "b1"}); err == nil {
		t.Fatal("expected error")
	}
	entries, _ := os.ReadDir(s.Dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("leftover temp file %s", e.Name())
		}
	}
}

func TestResaveReplaces(t *testing.T) {
	s := newTestStore(t)
	m := saveAt(t, s, "r1", t0)
	m.Command = "changed"
	if err := s.Save(m); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Load("r1")
	if got.Command != "changed" {
		t.Fatalf("command = %q", got.Command)
	}
}

func TestLoadPrefix(t *testing.T) {
	s := newTestStore(t)
	for _, id := range []string{"20260929-2245-aaaa", "20260929-2245-bbbb", "20260930-0001-cccc", "20260930-0001-cccc2"} {
		saveAt(t, s, id, t0)
	}
	tests := []struct {
		name, in, want string
		err            error
	}{
		{"unique prefix", "20260930-0001-cccc2", "20260930-0001-cccc2", nil},
		{"exact wins over prefix", "20260930-0001-cccc", "20260930-0001-cccc", nil},
		{"prefix shared with exact id is ambiguous", "20260930", "", ErrAmbiguous},
		{"ambiguous", "20260929-2245", "", ErrAmbiguous},
		{"none", "nope", "", ErrNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, err := s.Load(tt.in)
			if tt.err != nil {
				if !errors.Is(err, tt.err) {
					t.Fatalf("err = %v, want %v", err, tt.err)
				}
				return
			}
			if err != nil || m.ID != tt.want {
				t.Fatalf("got %v, %v", m, err)
			}
		})
	}
	_, err := s.Load("20260929-2245")
	if !strings.Contains(err.Error(), "20260929-2245-aaaa") || !strings.Contains(err.Error(), "20260929-2245-bbbb") {
		t.Errorf("ambiguous error does not list candidates: %v", err)
	}
	m, err := s.Load("20260929-2245-a")
	if err != nil || m.ID != "20260929-2245-aaaa" {
		t.Fatalf("unique prefix: %v %v", m, err)
	}
}

func TestLoadMissingDir(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Load("x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
	if _, err := s.Latest(); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Latest err = %v", err)
	}
}

func TestListOrderAndProblems(t *testing.T) {
	s := newTestStore(t)
	saveAt(t, s, "a", t0)
	saveAt(t, s, "c", t0.Add(time.Hour))
	saveAt(t, s, "b", t0.Add(time.Hour))
	writeRaw(t, s, "corrupt.json", "{not json")
	writeRaw(t, s, "old.json", `{"version":99,"id":"old"}`)
	writeRaw(t, s, ".c-123.tmp", "{half")
	writeRaw(t, s, "note.txt", "hi")

	list, problems, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, m := range list {
		ids = append(ids, m.ID)
	}
	if strings.Join(ids, ",") != "c,b,a" {
		t.Errorf("order = %v", ids)
	}
	if len(problems) != 2 {
		t.Fatalf("problems = %v", problems)
	}
	for _, p := range problems {
		if p.Err == nil || p.File == "" {
			t.Errorf("bad problem %+v", p)
		}
	}
	latest, err := s.Latest()
	if err != nil || latest.ID != "c" {
		t.Fatalf("Latest = %v, %v", latest, err)
	}
}

func TestListMissingDir(t *testing.T) {
	list, problems, err := newTestStore(t).List()
	if err != nil || len(list) != 0 || len(problems) != 0 {
		t.Fatalf("got %v %v %v", list, problems, err)
	}
}

func TestListUnreadableDir(t *testing.T) {
	f := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(f, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := NewStore(f).List(); err == nil {
		t.Fatal("expected error when sessions dir is a file")
	}
}

func writeRaw(t *testing.T, s *Store, name, content string) {
	t.Helper()
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.Dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestReclaimedAndCounts(t *testing.T) {
	m := NewManifest(t0, "x")
	if m.StartedAt.Location() != time.UTC || m.Version != ManifestVersion || m.ID == "" {
		t.Fatalf("bad manifest %+v", m)
	}
	m.Add(Entry{Status: StatusApplied, SizeBytes: 100, Restorable: true})
	m.Add(Entry{Status: StatusApplied, SizeBytes: 50})
	m.Add(Entry{Status: StatusFailed, SizeBytes: 999})
	m.Add(Entry{Status: StatusSkipped, SizeBytes: 999})
	m.Add(Entry{Status: StatusRestored, SizeBytes: 999})
	if m.ReclaimedBytes != 150 {
		t.Errorf("reclaimed = %d", m.ReclaimedBytes)
	}
	want := Counts{Applied: 2, Failed: 1, Skipped: 1, Restored: 1, Restorable: 1}
	if got := m.Counts(); got != want {
		t.Errorf("counts = %+v", got)
	}
	m.Entries[0].Status = StatusRestored
	m.RecomputeReclaimed()
	if m.ReclaimedBytes != 50 {
		t.Errorf("after restore reclaimed = %d", m.ReclaimedBytes)
	}
	if !m.FinishedAt.IsZero() {
		t.Error("FinishedAt set early")
	}
	m.Finish(t0)
	if m.FinishedAt.IsZero() {
		t.Error("Finish did not set")
	}
}
