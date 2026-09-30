package trash

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

var purgeNow = time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)

// writeQuarantineSession creates <dir>/<id>/ with one stored item of the given
// size and, when created is non-zero, a manifest.json claiming that size.
func writeQuarantineSession(t *testing.T, dir, id string, created time.Time, size int) string {
	t.Helper()
	sd := filepath.Join(dir, id)
	writeFile(t, filepath.Join(sd, "1", "f.txt"), string(make([]byte, size)), 0o600)
	if !created.IsZero() {
		m := QuarantineManifest{Version: ManifestVersion, SessionID: id, CreatedAt: created,
			Items: []QuarantineItem{{N: 1, OriginalPath: "/x/f.txt", StoredPath: "1/f.txt", SizeBytes: int64(size)}}}
		data, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(sd, ManifestName), string(data), 0o600)
	}
	return sd
}

func TestListQuarantine(t *testing.T) {
	dir := t.TempDir()
	day := 24 * time.Hour
	writeQuarantineSession(t, dir, "20260101-000000-aaaa", purgeNow.Add(-30*day), 100)
	writeQuarantineSession(t, dir, "20260102-000000-bbbb", purgeNow.Add(-15*day), 50)
	writeQuarantineSession(t, dir, "20260103-000000-cccc", purgeNow.Add(-13*day), 10)
	// Not session directories or not directories: never listed.
	writeFile(t, filepath.Join(dir, "notes", "x"), "keep", 0o600)
	writeFile(t, filepath.Join(dir, "20260101-000000-aaaa.txt"), "keep", 0o600)

	l, err := ListQuarantine(dir, purgeNow, 14)
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Expired) != 2 || l.Expired[0].ID != "20260101-000000-aaaa" || l.Expired[1].ID != "20260102-000000-bbbb" {
		t.Fatalf("expired = %+v", l.Expired)
	}
	if l.TotalBytes() != 150 || !l.Expired[0].FromManifest {
		t.Fatalf("total %d, %+v", l.TotalBytes(), l.Expired[0])
	}
}

func TestListQuarantineRetentionZeroAndMissingDir(t *testing.T) {
	dir := t.TempDir()
	writeQuarantineSession(t, dir, "20200101-000000-aaaa", purgeNow.AddDate(-5, 0, 0), 1)
	for _, days := range []int{0, -3} {
		l, err := ListQuarantine(dir, purgeNow, days)
		if err != nil || len(l.Expired) != 0 {
			t.Fatalf("days=%d: %+v %v", days, l, err)
		}
	}
	l, err := ListQuarantine(filepath.Join(dir, "missing"), purgeNow, 14)
	if err != nil || len(l.Expired) != 0 {
		t.Fatalf("missing dir: %+v %v", l, err)
	}
}

func TestListQuarantineFallsBackWithoutManifest(t *testing.T) {
	for name, damage := range map[string]func(sd string){
		"missing": func(string) {},
		"corrupt": func(sd string) { writeFile(t, filepath.Join(sd, ManifestName), "{not json", 0o600) },
		"no time": func(sd string) { writeFile(t, filepath.Join(sd, ManifestName), `{"version":1,"items":[]}`, 0o600) },
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			sd := writeQuarantineSession(t, dir, "20260101-000000-aaaa", time.Time{}, 42)
			damage(sd)
			old := purgeNow.Add(-40 * 24 * time.Hour)
			if err := os.Chtimes(sd, old, old); err != nil {
				t.Fatal(err)
			}
			l, err := ListQuarantine(dir, purgeNow, 14)
			if err != nil || len(l.Expired) != 1 {
				t.Fatalf("%+v %v", l, err)
			}
			s := l.Expired[0]
			if s.FromManifest || s.SizeBytes < 42 || !s.CreatedAt.Equal(old) {
				t.Fatalf("fallbacks not used: %+v", s)
			}
		})
	}
}

func TestListQuarantineReportsSymlinkedSessions(t *testing.T) {
	dir := t.TempDir()
	target := t.TempDir()
	symlinkOrSkip(t, target, filepath.Join(dir, "20260101-000000-aaaa"))
	l, err := ListQuarantine(dir, purgeNow, 14)
	if err != nil || len(l.Expired) != 0 || len(l.Skipped) != 1 {
		t.Fatalf("%+v %v", l, err)
	}
}

func TestPurgeDeletesOnlyTheGivenSessions(t *testing.T) {
	dir := t.TempDir()
	old := writeQuarantineSession(t, dir, "20260101-000000-aaaa", purgeNow.AddDate(0, -2, 0), 5)
	keep := writeQuarantineSession(t, dir, "20260301-000000-bbbb", purgeNow, 5)
	other := filepath.Join(dir, "other.txt")
	writeFile(t, other, "keep", 0o600)
	// A read-only file inside must not stop the removal (Windows).
	if err := os.Chmod(filepath.Join(old, "1", "f.txt"), 0o400); err != nil {
		t.Fatal(err)
	}

	l, _ := ListQuarantine(dir, purgeNow, 14)
	res := Purge(dir, l.Expired)
	if len(res) != 1 || res[0].Err != nil {
		t.Fatalf("%+v", res)
	}
	if exists(old) || !exists(keep) || !exists(other) {
		t.Fatalf("old=%v keep=%v other=%v", exists(old), exists(keep), exists(other))
	}
}

func TestPurgeRefusals(t *testing.T) {
	dir := t.TempDir()
	victim := t.TempDir()
	writeFile(t, filepath.Join(victim, "precious"), "x", 0o600)
	link := filepath.Join(dir, "20260101-000000-aaaa")
	symlinkOrSkip(t, victim, link)
	valid := writeQuarantineSession(t, dir, "20260102-000000-bbbb", purgeNow, 1)

	tests := []struct {
		name string
		s    QuarantinedSession
	}{
		{"symlinked session dir", QuarantinedSession{ID: "20260101-000000-aaaa", Dir: link}},
		{"bad id", QuarantinedSession{ID: "..", Dir: dir}},
		{"id with separator", QuarantinedSession{ID: "../20260102-000000-bbbb", Dir: filepath.Join(dir, "..", "20260102-000000-bbbb")}},
		{"dir differs from id", QuarantinedSession{ID: "20260102-000000-bbbb", Dir: victim}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := Purge(dir, []QuarantinedSession{tt.s})
			if res[0].Err == nil {
				t.Fatal("expected a refusal")
			}
		})
	}
	if !exists(filepath.Join(victim, "precious")) || !exists(valid) {
		t.Fatal("a refused purge deleted something")
	}
}

func TestPurgeMissingSessionIsDone(t *testing.T) {
	dir := t.TempDir()
	res := Purge(dir, []QuarantinedSession{{ID: "20260101-000000-aaaa", Dir: filepath.Join(dir, "20260101-000000-aaaa")}})
	if res[0].Err != nil {
		t.Fatal(res[0].Err)
	}
}
