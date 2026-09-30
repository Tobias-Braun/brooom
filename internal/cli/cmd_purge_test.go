package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tobias-Braun/brooom/internal/output"
	"github.com/Tobias-Braun/brooom/internal/walk"
)

// allocatedSizeOf is the size string sessions report for a file with the given
// content: its allocation on this filesystem (one block for small files), not
// the number of bytes written.
func allocatedSizeOf(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "probe")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	return output.FormatSize(walk.AllocatedSize(fi))
}

var purgeClock = time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)

const (
	oldSession = "20260801-100000-aaaa"
	newSession = "20260929-100000-bbbb"
)

// agedFixture has one 60 day old and one 1 day old quarantined session, a
// stray file and directory in the quarantine dir, and the default retention
// of 14 days.
func agedFixture(t *testing.T) *undoFixture {
	t.Helper()
	f := newUndoFixture(t)
	f.session(oldSession, purgeClock.Add(-60*24*time.Hour), f.write("old.txt", "old data"))
	f.session(newSession, purgeClock.Add(-24*time.Hour), f.write("new.txt", "new"))
	// The trasher stamped both manifests with the real clock; age them.
	ageQuarantine(t, f, oldSession, purgeClock.Add(-60*24*time.Hour))
	ageQuarantine(t, f, newSession, purgeClock.Add(-24*time.Hour))
	for _, p := range []string{"notes.txt", filepath.Join("scratch", "x")} {
		full := filepath.Join(f.quarantineDir(), p)
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("mine"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

// ageQuarantine rewrites created_at of a quarantine manifest.
func ageQuarantine(t *testing.T, f *undoFixture, id string, created time.Time) {
	t.Helper()
	path := filepath.Join(f.quarantineDir(), id, "manifest.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		if strings.Contains(line, `"created_at"`) {
			line = `  "created_at": "` + created.Format(time.RFC3339) + `",`
		}
		out = append(out, line)
	}
	if err := os.WriteFile(path, []byte(strings.Join(out, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestPurgeDryRunListsExpiredOnly(t *testing.T) {
	f := agedFixture(t)
	code, out, _ := runApp(t, "", false, purgeClock, "purge", "--dry-run")
	if code != ExitOK {
		t.Fatalf("code=%d out=%s", code, out)
	}
	for _, want := range []string{oldSession, "60 days old", "older than 14 days", "total: 1 session(s), " + allocatedSizeOf(t, "old data"),
		"dry run: nothing was deleted; re-run 'brooom purge' without --dry-run to delete them permanently"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, newSession) {
		t.Fatalf("a young session is listed:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(f.quarantineDir(), oldSession)); err != nil {
		t.Fatal("dry run deleted the session")
	}
}

func TestPurgeApplyDeletesAndMarksManifests(t *testing.T) {
	f := agedFixture(t)
	code, out, errOut := runApp(t, "", false, purgeClock, "purge", "--yes")
	if code != ExitOK {
		t.Fatalf("code=%d out=%s err=%s", code, out, errOut)
	}
	q := f.quarantineDir()
	if _, err := os.Lstat(filepath.Join(q, oldSession)); err == nil {
		t.Fatal("expired session still there")
	}
	for _, keep := range []string{newSession, "notes.txt", filepath.Join("scratch", "x")} {
		if _, err := os.Lstat(filepath.Join(q, keep)); err != nil {
			t.Fatalf("%s must be untouched: %v", keep, err)
		}
	}
	e := f.reload(oldSession).Entries[0]
	if e.Restorable || e.RecoveryHint != "purged from quarantine on 2026-09-30" {
		t.Fatalf("manifest entry not marked: %+v", e)
	}
	if !f.reload(newSession).Entries[0].Restorable {
		t.Fatal("young session's entry must stay restorable")
	}
	if !strings.Contains(out, "purged 1 session(s), freed "+allocatedSizeOf(t, "old data")) {
		t.Fatalf("output:\n%s", out)
	}
	// undo now lists the purged entry as not restorable with the hint.
	code, out, _ = runApp(t, "", false, purgeClock, "undo", oldSession, "--dry-run")
	if code != ExitOK || !strings.Contains(out, "cannot restore") || !strings.Contains(out, "purged from quarantine on 2026-09-30") {
		t.Fatalf("undo after purge: code=%d\n%s", code, out)
	}
}

func TestPurgeConfirmation(t *testing.T) {
	tests := []struct {
		name     string
		stdin    string
		tty      bool
		wantCode int
		deleted  bool
	}{
		{"accepted", "y\n", true, ExitOK, true},
		{"declined", "n\n", true, ExitOK, false},
		{"non-interactive without --yes", "y\n", false, ExitUsage, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := agedFixture(t)
			code, out, _ := runApp(t, tt.stdin, tt.tty, purgeClock, "purge")
			if code != tt.wantCode {
				t.Fatalf("code=%d out=%s", code, out)
			}
			_, err := os.Lstat(filepath.Join(f.quarantineDir(), oldSession))
			if gone := err != nil; gone != tt.deleted {
				t.Fatalf("deleted=%v, want %v", gone, tt.deleted)
			}
			if tt.name == "declined" && f.reload(oldSession).Entries[0].Restorable != true {
				t.Fatal("declined purge changed the manifest")
			}
		})
	}
}

func TestPurgeRetentionZeroListsNothing(t *testing.T) {
	f := agedFixture(t)
	writeConfig(t, f.home, map[string]any{"trash": map[string]any{"quarantine_retention_days": 0}})
	code, out, _ := runApp(t, "", false, purgeClock, "purge", "--yes")
	if code != ExitOK || !strings.Contains(out, "never expire") {
		t.Fatalf("code=%d out=%s", code, out)
	}
	if _, err := os.Lstat(filepath.Join(f.quarantineDir(), oldSession)); err != nil {
		t.Fatal("retention 0 deleted a session")
	}
}

func TestPurgeUsesConfiguredRetention(t *testing.T) {
	f := agedFixture(t)
	writeConfig(t, f.home, map[string]any{"trash": map[string]any{"quarantine_retention_days": 1}})
	_, out, _ := runApp(t, "", false, purgeClock, "purge", "--dry-run")
	// 24 hours old is not older than 1 day yet; the 60 day old one is.
	if !strings.Contains(out, "older than 1 days") || strings.Contains(out, newSession) || !strings.Contains(out, oldSession) {
		t.Fatalf("output:\n%s", out)
	}
}

func TestPurgeNothingExpired(t *testing.T) {
	newUndoFixture(t)
	code, out, _ := runApp(t, "", false, purgeClock, "purge", "--yes")
	if code != ExitOK || !strings.Contains(out, "nothing to purge") {
		t.Fatalf("code=%d out=%s", code, out)
	}
}

func TestPurgeSkipsSymlinkedSessionDir(t *testing.T) {
	f := newUndoFixture(t)
	victim := t.TempDir()
	if err := os.WriteFile(filepath.Join(victim, "precious"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(f.quarantineDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(f.quarantineDir(), oldSession)); err != nil {
		t.Skipf("symlinks not available: %v", err)
	}
	code, out, errOut := runApp(t, "", false, purgeClock, "purge", "--yes")
	if code != ExitOK || !strings.Contains(errOut, "skipping") {
		t.Fatalf("code=%d out=%s err=%s", code, out, errOut)
	}
	if _, err := os.Stat(filepath.Join(victim, "precious")); err != nil {
		t.Fatal("followed a symlink")
	}
}

func TestPurgeManifestlessSessionUsesMtime(t *testing.T) {
	f := newUndoFixture(t)
	dir := filepath.Join(f.quarantineDir(), oldSession, "1")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "f"), []byte("12345"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := purgeClock.Add(-90 * 24 * time.Hour)
	if err := os.Chtimes(filepath.Join(f.quarantineDir(), oldSession), old, old); err != nil {
		t.Fatal(err)
	}
	code, out, _ := runApp(t, "", false, purgeClock, "purge", "--dry-run")
	if code != ExitOK || !strings.Contains(out, oldSession) || !strings.Contains(out, "90 days old") {
		t.Fatalf("code=%d out=%s", code, out)
	}
}
