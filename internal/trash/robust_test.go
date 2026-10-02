package trash

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestRecycledPrefixLen pins the length of the re-rooted directory part:
// <vol>\$Recycle.Bin\<sid>\$R<6 random><ext>.
func TestRecycledPrefixLen(t *testing.T) {
	const sid = "S-1-5-21-1111111111-2222222222-3333333333-1001" // 46 chars
	tests := []struct {
		name string
		path string
		want int
	}{
		{"file with extension", `C:\a\b.txt`, 2 + 1 + 12 + 1 + 46 + 1 + 8 + 4},
		{"directory without dot", `C:\a\proj`, 2 + 1 + 12 + 1 + 46 + 1 + 8},
		{"non-ASCII extension counts UTF-16 units", `C:\a\x.日本`, 2 + 1 + 12 + 1 + 46 + 1 + 8 + 3},
		{"other volume spelling", `D:\x`, 2 + 1 + 12 + 1 + 46 + 1 + 8},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := recycledPrefixLen(tt.path, sid); got != tt.want {
				t.Errorf("recycledPrefixLen(%q) = %d, want %d", tt.path, got, tt.want)
			}
		})
	}
}

// TestCheckTreeDepth covers issue #122: only the top-level path was
// length-checked, so descendants re-rooted below $Recycle.Bin\<sid>\$R...
// overflowed MAX_PATH and made the shell block on its dialog.
func TestCheckTreeDepth(t *testing.T) {
	const sid = "S-1-5-21-1111111111-2222222222-3333333333-1001"
	top := `C:\p`
	prefix := recycledPrefixLen(top, sid) // 70 for a name without a dot
	tests := []struct {
		name       string
		path       string
		longestRel int
		wantErr    string
	}{
		{"file", top, 0, ""},
		{"shallow tree", top, 50, ""},
		{"fits exactly in the bin", top, maxShellPath - prefix, ""},
		{"one unit too deep in the bin", top, maxShellPath - prefix + 1, "inside the Recycle Bin"},
		{"short original but deep tree", top, 200, "inside the Recycle Bin"},
		{"descendant already over the limit", `C:\` + strings.Repeat("d", 100), 200, "contains a path of"},
		{"exactly the limit before re-rooting", `C:\` + strings.Repeat("d", 10), maxShellPath - len(`C:\`+strings.Repeat("d", 10)), "inside the Recycle Bin"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkTreeDepth(tt.path, sid, tt.longestRel)
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("unexpected refusal: %v", err)
			case tt.wantErr != "" && err == nil:
				t.Fatal("deep tree accepted")
			case tt.wantErr != "":
				if !strings.Contains(err.Error(), tt.wantErr) || !strings.Contains(err.Error(), manualHint) {
					t.Errorf("error %q lacks %q or the manual hint", err, tt.wantErr)
				}
			}
		})
	}
}

// TestMeasureTree checks that one walk yields the size and the UTF-16 length
// of the longest relative descendant path with its leading separator.
func TestMeasureTree(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	writeFile(t, filepath.Join(root, "a.txt"), "12345", 0o644)
	deep := filepath.Join(root, "dir", "日本語")
	writeFile(t, deep, "xy", 0o644)
	size, rel, err := measureTree(root)
	if err != nil {
		t.Fatal(err)
	}
	if want := refSize(t, root); size != want {
		t.Errorf("size = %d, want %d", size, want)
	}
	want := 1 + len("dir") + 1 + 3 // separator, "dir", separator, three BMP characters
	if rel != want {
		t.Errorf("longestRel = %d, want %d", rel, want)
	}
	file := filepath.Join(root, "a.txt")
	if _, rel, err := measureTree(file); err != nil || rel != 0 {
		t.Errorf("file: rel = %d, err = %v", rel, err)
	}
}

// TestCallBounded covers the bounded shell call: a result is passed through,
// and a call that never returns is abandoned on timeout and on cancellation.
func TestCallBounded(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	blocked := func() (int, bool) { <-release; return 0, false }

	code, aborted, err := callBounded(context.Background(), time.Second, func() (int, bool) { return 7, true })
	if err != nil || code != 7 || !aborted {
		t.Errorf("result = (%d, %v, %v)", code, aborted, err)
	}

	start := time.Now()
	_, _, err = callBounded(context.Background(), 20*time.Millisecond, blocked)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("timeout err = %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Error("timeout was not honoured")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err = callBounded(ctx, time.Minute, blocked)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("cancel err = %v", err)
	}
}

// TestCollectEntriesReadsEachInfoOnce covers issue #136 item 2: N listings of
// a bin with M items must not read the M $I files N times.
func TestCollectEntriesReadsEachInfoOnce(t *testing.T) {
	var reads atomic.Int32
	read := func(name string) (infoRecord, bool, bool) {
		reads.Add(1)
		if name == "$Ibad" {
			return infoRecord{}, false, false
		}
		return infoRecord{Path: `C:\x\` + name}, true, true
	}
	names := []string{"$Ia", "$Ib", "$Ibad"}
	var cache infoCache
	for i := 0; i < 5; i++ {
		got := collectEntries(`C:\$Recycle.Bin\S-1`, names, &cache, read)
		if len(got) != 2 || got[0].Info.Path != `C:\x\$Ia` {
			t.Fatalf("listing %d = %+v", i, got)
		}
	}
	// Two cached items are read once; the unusable one is retried each time.
	if want := int32(2 + 5); reads.Load() != want {
		t.Errorf("reads = %d, want %d", reads.Load(), want)
	}
	reads.Store(0)
	for i := 0; i < 3; i++ {
		collectEntries(`C:\$Recycle.Bin\S-1`, names, nil, read)
	}
	if reads.Load() != 9 {
		t.Errorf("without a cache reads = %d, want 9", reads.Load())
	}
}

// TestAnnotateLocked checks that lock errors name the file and others pass
// through unchanged.
func TestAnnotateLocked(t *testing.T) {
	base := errors.New("Access is denied.")
	pe := &fs.PathError{Op: "remove", Path: `D:\proj\a.lock`, Err: base}
	always := func(error) bool { return true }
	never := func(error) bool { return false }

	got := annotateLocked(pe, always)
	if !strings.Contains(got.Error(), `"D:\proj\a.lock" is in use or not permitted`) || !errors.Is(got, base) {
		t.Errorf("annotated = %v", got)
	}
	if !errors.Is(annotateLocked(pe, never), pe) {
		t.Error("non-lock error was changed")
	}
	if annotateLocked(nil, always) != nil {
		t.Error("nil was annotated")
	}
	if got := annotateLocked(base, always); !strings.Contains(got.Error(), "in use or not permitted") {
		t.Errorf("bare error = %v", got)
	}
}

// TestCheckNotInUse checks the pre-copy probe: an open item is refused,
// unknown answers are allowed.
func TestCheckNotInUse(t *testing.T) {
	src := filepath.Join(t.TempDir(), "proj")
	tests := []struct {
		name    string
		open    map[string]bool
		err     error
		wantErr bool
	}{
		{"open", map[string]bool{src: true}, nil, true},
		{"closed", map[string]bool{src: false}, nil, false},
		{"unavailable", nil, errors.New("unavailable"), false},
		{"incomplete but positive", map[string]bool{src: true}, errors.New("incomplete"), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkNotInUse(context.Background(), src, func(context.Context, []string) (map[string]bool, error) {
				return tt.open, tt.err
			})
			if (err != nil) != tt.wantErr {
				t.Errorf("err = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// TestMoveTreeRefusesOpenTreeBeforeCopying checks that a locked tree stops a
// cross-device move before anything is copied or removed.
func TestMoveTreeRefusesOpenTreeBeforeCopying(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	writeFile(t, filepath.Join(src, "a"), "a", 0o644)
	failCrossDevice(t)
	orig := probeInUse
	probeInUse = func(context.Context, string) error { return errors.New("in use") }
	t.Cleanup(func() { probeInUse = orig })

	dst := filepath.Join(root, "dst")
	if err := moveTree(context.Background(), src, dst); err == nil {
		t.Fatal("move of an open tree succeeded")
	}
	if !exists(filepath.Join(src, "a")) || exists(dst) {
		t.Error("the refused move touched the filesystem")
	}
}

// TestMoveTreeSourceRemovalErrorPassesThrough checks that a failing source
// removal still yields a SourceNotRemovedError on every OS. A real lock cannot
// be provoked everywhere; the Windows test file covers that case.
func TestMoveTreeSourceRemovalErrorPassesThrough(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	writeFile(t, filepath.Join(src, "a"), "a", 0o644)
	failCrossDevice(t)
	failSourceRemoval(t, filepath.Join(src, "a"))
	var partial *SourceNotRemovedError
	if err := moveTree(context.Background(), src, filepath.Join(root, "dst")); !errors.As(err, &partial) {
		t.Fatalf("err = %v, want SourceNotRemovedError", err)
	}
}

// TestCheckCopyableAcceptsPlainTrees checks that files, directories and
// symlinks, which copyTree reproduces, are never refused by the pre-copy walk.
// The refusal side is covered per OS (fifo on unix, junction on Windows).
func TestCheckCopyableAcceptsPlainTrees(t *testing.T) {
	root := filepath.Join(t.TempDir(), "tree")
	writeFile(t, filepath.Join(root, "a", "b"), "x", 0o644)
	if err := os.Mkdir(filepath.Join(root, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	symlinkOrSkip(t, "a", filepath.Join(root, "link"))
	if err := checkCopyable(root); err != nil {
		t.Fatalf("plain tree refused: %v", err)
	}
}

// TestInfoCacheKeyedByBinDir covers a trasher that spans volumes: two bins
// holding the same $I name with different content must not share a result.
func TestInfoCacheKeyedByBinDir(t *testing.T) {
	read := func(dir string) func(string) (infoRecord, bool, bool) {
		return func(name string) (infoRecord, bool, bool) {
			return infoRecord{Path: dir + `\` + name}, true, true
		}
	}
	const c, d = `C:\$Recycle.Bin\S-1`, `D:\$Recycle.Bin\S-1`
	var cache infoCache
	names := []string{"$IABC123.txt"}
	gotC := collectEntries(c, names, &cache, read("c"))
	gotD := collectEntries(d, names, &cache, read("d"))
	if len(gotC) != 1 || len(gotD) != 1 || gotC[0].Info.Path != `c\$IABC123.txt` || gotD[0].Info.Path != `d\$IABC123.txt` {
		t.Fatalf("shared cache entry: C = %+v, D = %+v", gotC, gotD)
	}
	// The same directory in another case is the same bin and hits the cache.
	again := collectEntries(strings.ToLower(c), []string{"$iabc123.TXT"}, &cache, read("x"))
	if len(again) != 1 || again[0].Info.Path != `c\$IABC123.txt` {
		t.Errorf("case-folded lookup = %+v", again)
	}
}
