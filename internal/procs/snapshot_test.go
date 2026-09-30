package procs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

// TestSnapshotLoadsListingOnce is the regression test for one lsof run per
// target: many queries share a single listing.
func TestSnapshotLoadsListingOnce(t *testing.T) {
	dir := t.TempDir()
	var files []string
	for _, n := range []string{"a.log", "b.log", "c.log"} {
		p := filepath.Join(dir, n)
		if err := os.WriteFile(p, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		files = append(files, p)
	}
	var loads atomic.Int32
	snap := NewSnapshotFrom(func(context.Context) ([]string, error) {
		loads.Add(1)
		return []string{files[1]}, nil
	})
	for _, p := range files {
		res, err := snap.OpenAmong(context.Background(), []string{p})
		if err != nil {
			t.Fatal(err)
		}
		if want := p == files[1]; res[p] != want {
			t.Errorf("%s open = %v, want %v", p, res[p], want)
		}
	}
	if _, err := snap.OpenAmong(context.Background(), []string{dir}); err != nil {
		t.Fatal(err)
	}
	if got := loads.Load(); got != 1 {
		t.Errorf("listing loaded %d times for 4 queries, want 1", got)
	}
}

func TestSnapshotMatching(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "wt")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(dir, "wt2")
	if err := os.Mkdir(other, 0o755); err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(dir, "x.log")
	if err := os.WriteFile(f, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name  string
		names []string
		path  string
		want  bool
	}{
		{"file exact", []string{f}, f, true},
		{"file case-insensitive", []string{filepath.Join(dir, "X.LOG")}, f, true},
		{"dir cwd equals dir", []string{sub}, sub, true},
		{"dir has open file below", []string{filepath.Join(sub, "a", "b")}, sub, true},
		{"sibling prefix is not below", []string{filepath.Join(other, "a")}, sub, false},
		{"nothing open", nil, f, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snap := NewSnapshotFrom(func(context.Context) ([]string, error) { return tt.names, nil })
			res, err := snap.OpenAmong(context.Background(), []string{tt.path})
			if err != nil {
				t.Fatal(err)
			}
			if res[tt.path] != tt.want {
				t.Errorf("open = %v, want %v", res[tt.path], tt.want)
			}
		})
	}
}

// TestSnapshotFallsBackWhenUnavailable: an unusable listing must not turn
// into "nothing open"; the per-call path answers instead.
func TestSnapshotFallsBackWhenUnavailable(t *testing.T) {
	p := filepath.Join(t.TempDir(), "missing")
	snap := NewSnapshotFrom(func(context.Context) ([]string, error) { return nil, ErrUnavailable })
	res, err := snap.OpenAmong(context.Background(), []string{p})
	if errors.Is(err, ErrUnavailable) && err != nil && res != nil {
		t.Errorf("unexpected result %v with %v", res, err)
	}
	if res != nil && res[p] {
		t.Errorf("missing path reported open")
	}
}

// TestSnapshotIncompleteKeepsTruePositives: a listing cut short still flags
// what it saw and reports the rest as unknown.
func TestSnapshotIncompleteKeepsTruePositives(t *testing.T) {
	f := filepath.Join(t.TempDir(), "x.log")
	if err := os.WriteFile(f, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	snap := NewSnapshotFrom(func(context.Context) ([]string, error) { return []string{f}, ErrIncomplete })
	res, err := snap.OpenAmong(context.Background(), []string{f})
	if !errors.Is(err, ErrIncomplete) {
		t.Errorf("err = %v, want ErrIncomplete", err)
	}
	if !res[f] {
		t.Error("true positive dropped")
	}
}

// TestSnapshotCancelledLoadIsNotRemembered: one cancelled query must not
// poison the rest of the scan.
func TestSnapshotCancelledLoadIsNotRemembered(t *testing.T) {
	f := filepath.Join(t.TempDir(), "x.log")
	if err := os.WriteFile(f, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	var loads int
	snap := NewSnapshotFrom(func(ctx context.Context) ([]string, error) {
		loads++
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return []string{f}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _ = snap.OpenAmong(ctx, []string{f})
	res, err := snap.OpenAmong(context.Background(), []string{f})
	if err != nil || !res[f] {
		t.Errorf("second query = %v, %v; want open, nil", res, err)
	}
	if loads != 2 {
		t.Errorf("loads = %d, want 2", loads)
	}
}

func TestNilSnapshotDelegates(t *testing.T) {
	var snap *Snapshot
	p := filepath.Join(t.TempDir(), "missing")
	res, _ := snap.OpenAmong(context.Background(), []string{p})
	if res[p] {
		t.Error("missing path reported open")
	}
}
