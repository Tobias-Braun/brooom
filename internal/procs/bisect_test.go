package procs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// oracle is a fake Restart Manager: a batch is "locked" when it contains a
// locked file, and files listed in bad make any batch containing them fail.
type oracle struct {
	locked  map[string]bool
	bad     map[string]bool
	queries int
	fatal   error
}

func (o *oracle) query(files []string) (bool, error) {
	o.queries++
	if o.fatal != nil {
		return false, o.fatal
	}
	hit := false
	for _, f := range files {
		if o.bad[f] {
			return false, fmt.Errorf("rejected %s", f)
		}
		hit = hit || o.locked[f]
	}
	return hit, nil
}

func names(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("f%03d", i)
	}
	return out
}

func set(items ...string) map[string]bool {
	m := map[string]bool{}
	for _, i := range items {
		m[i] = true
	}
	return m
}

func TestBisectLocked(t *testing.T) {
	tests := []struct {
		name        string
		n           int
		locked, bad map[string]bool
		wantHits    []string
		wantUnknown []string
	}{
		{"empty", 0, nil, nil, nil, nil},
		{"none locked", 40, nil, nil, nil, nil},
		{"one locked", 40, set("f017"), nil, []string{"f017"}, nil},
		{"first and last", 40, set("f000", "f039"), nil, []string{"f000", "f039"}, nil},
		{"all locked", 5, set(names(5)...), nil, names(5), nil},
		{"single locked", 1, set("f000"), nil, []string{"f000"}, nil},
		{"rejected file is unknown, others judged", 8, set("f001"), set("f006"), []string{"f001"}, []string{"f006"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := &oracle{locked: tt.locked, bad: tt.bad}
			hits, unknown, err := bisectLocked(context.Background(), names(tt.n), false, o.query)
			if err != nil {
				t.Fatal(err)
			}
			sort.Strings(hits)
			if !reflect.DeepEqual(hits, tt.wantHits) || !reflect.DeepEqual(unknown, tt.wantUnknown) {
				t.Errorf("hits %v unknown %v, want %v / %v", hits, unknown, tt.wantHits, tt.wantUnknown)
			}
		})
	}
}

func TestBisectQueryCountIsLogarithmic(t *testing.T) {
	o := &oracle{locked: set("f100")}
	if _, _, err := bisectLocked(context.Background(), names(256), false, o.query); err != nil {
		t.Fatal(err)
	}
	// 1 for the whole batch plus at most one per level (8 levels); the
	// inference for the right half saves the rest.
	if o.queries > 20 {
		t.Errorf("too many queries: %d", o.queries)
	}
}

func TestBisectFatalErrorAborts(t *testing.T) {
	o := &oracle{fatal: fmt.Errorf("%w: no session", ErrUnavailable)}
	_, _, err := bisectLocked(context.Background(), names(10), false, o.query)
	if !errors.Is(err, ErrUnavailable) {
		t.Errorf("got %v, want ErrUnavailable", err)
	}
}

func TestBisectCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	o := &oracle{locked: set("f001")}
	_, unknown, err := bisectLocked(ctx, names(4), false, o.query)
	if !errors.Is(err, ErrIncomplete) || len(unknown) != 4 || o.queries != 0 {
		t.Errorf("err %v unknown %v queries %d", err, unknown, o.queries)
	}
}

func TestCheckBatchesSpansBatches(t *testing.T) {
	all := names(lockBatch*2 + 10)
	o := &oracle{locked: set(all[3], all[lockBatch+5], all[len(all)-1])}
	res := map[string]bool{}
	incomplete, err := checkBatches(context.Background(), all, o.query, res)
	if err != nil || incomplete {
		t.Fatalf("err %v incomplete %v", err, incomplete)
	}
	if len(res) != 3 || !res[all[3]] || !res[all[len(all)-1]] {
		t.Errorf("got %v", res)
	}
}

func TestAnyLocked(t *testing.T) {
	tests := []struct {
		name           string
		o              *oracle
		wantHit, wantI bool
	}{
		{"none", &oracle{}, false, false},
		{"hit", &oracle{locked: set("f005")}, true, false},
		{"rejected without hit is incomplete", &oracle{bad: set("f002")}, false, true},
		{"hit despite rejected sibling", &oracle{bad: set("f002"), locked: set("f007")}, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hit, inc, err := anyLocked(context.Background(), names(10), tt.o.query)
			if err != nil || hit != tt.wantHit || inc != tt.wantI {
				t.Errorf("hit %v inc %v err %v", hit, inc, err)
			}
		})
	}
}

func TestListFilesBelow(t *testing.T) {
	root := t.TempDir()
	mk := func(rel string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	mk("a.txt")
	mk("sub/b.txt")
	mk("sub/deeper/c.txt")

	files, truncated := listFilesBelow(context.Background(), root)
	if truncated || len(files) != 3 {
		t.Errorf("got %v truncated %v", files, truncated)
	}
}

func TestListFilesBelowSkipsSymlinkedDirs(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Skipf("cannot create symlinks here: %v", err)
	}
	files, _ := listFilesBelow(context.Background(), root)
	if len(files) != 0 {
		t.Errorf("walked through a symlink: %v", files)
	}
}

func TestListFilesBelowCaps(t *testing.T) {
	t.Run("file cap", func(t *testing.T) {
		root := t.TempDir()
		for i := 0; i < dirMaxFiles+5; i++ {
			if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("f%04d", i)), nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		files, truncated := listFilesBelow(context.Background(), root)
		if !truncated || len(files) != dirMaxFiles {
			t.Errorf("got %d files truncated %v", len(files), truncated)
		}
	})
	t.Run("depth cap", func(t *testing.T) {
		root := t.TempDir()
		deep := filepath.Join(append([]string{root}, strings.Split(strings.Repeat("d,", dirMaxDepth+2), ",")[:dirMaxDepth+2]...)...)
		if err := os.MkdirAll(deep, 0o755); err != nil {
			t.Fatal(err)
		}
		_, truncated := listFilesBelow(context.Background(), root)
		if !truncated {
			t.Error("expected truncation for a tree deeper than the cap")
		}
	})
}

func TestLockedDirs(t *testing.T) {
	root := t.TempDir()
	busy := filepath.Join(root, "busy")
	idle := filepath.Join(root, "idle")
	for _, d := range []string{busy, idle} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	held := filepath.Join(busy, "held.log")
	for _, f := range []string{held, filepath.Join(idle, "free.log")} {
		if err := os.WriteFile(f, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	o := &oracle{locked: set(held)}
	res := map[string]bool{busy: false, idle: false}
	incomplete, err := lockedDirs(context.Background(), []string{busy, idle}, o.query, res)
	if err != nil || incomplete {
		t.Fatalf("err %v incomplete %v", err, incomplete)
	}
	if !res[busy] || res[idle] {
		t.Errorf("got %v", res)
	}
}

func TestExtendedPath(t *testing.T) {
	long := `C:\` + strings.Repeat("a", 300)
	tests := []struct{ in, want string }{
		{`C:\short\file.log`, `C:\short\file.log`},
		{long, `\\?\` + long},
		{`\\?\` + long, `\\?\` + long},
		{`\\server\share\` + strings.Repeat("b", 300), `\\?\UNC\server\share\` + strings.Repeat("b", 300)},
	}
	for _, tt := range tests {
		if got := extendedPath(tt.in); got != tt.want {
			t.Errorf("extendedPath(%.30q) = %.40q, want %.40q", tt.in, got, tt.want)
		}
	}
}
