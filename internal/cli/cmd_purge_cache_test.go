package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// staleCacheFile plants a cache file for a root that does not exist, which
// counts as stale whatever its age.
func staleCacheFile(t *testing.T, f *undoFixture) string {
	t.Helper()
	dir := filepath.Join(f.home, "cache")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	gone := filepath.Join(t.TempDir(), "vanished-root")
	path := filepath.Join(dir, "dirsize-v1-0123456789abcdef.json")
	body := `{"version":5,"root":` + jsonString(gone) + `,"dirs":{}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func jsonString(s string) string {
	return `"` + strings.ReplaceAll(s, `\`, `\\`) + `"`
}

func TestPurgeListsStaleScanCacheInDryRun(t *testing.T) {
	f := newUndoFixture(t)
	path := staleCacheFile(t, f)
	code, out, _ := runApp(t, "", false, purgeClock, "purge")
	if code != ExitOK {
		t.Fatalf("code=%d out=%s", code, out)
	}
	for _, want := range []string{"stale scan cache files:", "dirsize-v1-0123456789abcdef.json", "root no longer exists", "dry run: nothing was deleted"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("dry run deleted the cache file")
	}
}

func TestPurgeApplyRemovesStaleScanCacheOnly(t *testing.T) {
	f := newUndoFixture(t)
	path := staleCacheFile(t, f)
	keep := filepath.Join(filepath.Dir(path), "notes.txt")
	if err := os.WriteFile(keep, []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-90 * 24 * time.Hour)
	_ = os.Chtimes(keep, old, old)
	code, out, errOut := runApp(t, "", false, purgeClock, "purge", "--apply", "--yes")
	if code != ExitOK {
		t.Fatalf("code=%d out=%s err=%s", code, out, errOut)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("stale cache file survived: %v", err)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("unrelated file in the cache dir was touched: %v", err)
	}
	if !strings.Contains(out, "removed 1 cache file(s)") {
		t.Errorf("output:\n%s", out)
	}
}

func TestPurgeConfirmationDeclinedKeepsCache(t *testing.T) {
	f := newUndoFixture(t)
	path := staleCacheFile(t, f)
	code, out, _ := runApp(t, "n\n", true, purgeClock, "purge", "--apply")
	if code != ExitOK || !strings.Contains(out, "aborted") {
		t.Fatalf("code=%d out=%s", code, out)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("declined purge deleted the cache file")
	}
}
