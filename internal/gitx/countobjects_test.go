package gitx_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/gitx"
	"github.com/Tobias-Braun/brooom/internal/testutil"
)

func TestParseCountObjects(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    gitx.ObjectStats
		wantErr bool
	}{
		{
			name: "full sample",
			in:   "count: 1234\nsize: 5000\nin-pack: 99\npacks: 3\nsize-pack: 2048\nprune-packable: 7\ngarbage: 2\nsize-garbage: 8\n",
			want: gitx.ObjectStats{Count: 1234, Size: 5000 * 1024, InPack: 99, Packs: 3, SizePack: 2048 * 1024, PrunePackable: 7, Garbage: 2, SizeGarbage: 8 * 1024},
		},
		{
			name: "crlf and alternate ignored",
			in:   "count: 1\r\nsize: 4\r\nin-pack: 0\r\npacks: 0\r\nsize-pack: 0\r\nprune-packable: 0\r\ngarbage: 0\r\nsize-garbage: 0\r\nalternate: /some/where/objects\r\n",
			want: gitx.ObjectStats{Count: 1, Size: 4096},
		},
		{name: "empty", in: "", want: gitx.ObjectStats{}},
		{name: "unknown key", in: "future-key: 5\ncount: 2\n", want: gitx.ObjectStats{Count: 2}},
		{name: "not a number", in: "count: many\n", wantErr: true},
		{name: "negative", in: "packs: -1\n", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := gitx.ParseCountObjects(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v", err)
			}
			if got != tt.want {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestCountObjectsRealRepo(t *testing.T) {
	runner := execRunner(t)
	r := testutil.NewRepo(t)
	stats, err := gitx.CountObjects(context.Background(), runner, r.Dir)
	if err != nil {
		t.Fatal(err)
	}
	// Initial commit: blob, tree and commit, all loose.
	if stats.Count < 3 || stats.InPack != 0 || stats.Packs != 0 {
		t.Errorf("fresh repo: %+v", stats)
	}
	r.Git("-c", "gc.auto=0", "repack", "-d", "-q")
	stats, err = gitx.CountObjects(context.Background(), runner, r.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Count != 0 || stats.Packs != 1 || stats.InPack < 3 || stats.SizePack <= 0 {
		t.Errorf("after repack: %+v", stats)
	}
}

func TestCountObjectsNotARepo(t *testing.T) {
	runner := execRunner(t)
	if _, err := gitx.CountObjects(context.Background(), runner, testutil.ResolvedTempDir(t)); err == nil {
		t.Fatal("want error outside a repository")
	}
}

func TestRepoCountObjectsAndMemoShared(t *testing.T) {
	runner := execRunner(t)
	r := testutil.NewRepo(t)
	cache := gitx.NewCache(runner)
	a, err := cache.Repo(context.Background(), r.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.CountObjects(context.Background()); err != nil {
		t.Fatal(err)
	}
	calls := 0
	for i := 0; i < 3; i++ {
		v, err := a.Memo("k", func() (any, error) { calls++; return "v", nil })
		if err != nil || v.(string) != "v" {
			t.Fatalf("memo: %v %v", v, err)
		}
	}
	if calls != 1 {
		t.Errorf("memo ran %d times", calls)
	}
	// Uncached handles never memoize.
	u := openRepo(t, runner, r.Dir)
	for i := 0; i < 2; i++ {
		_, _ = u.Memo("k", func() (any, error) { calls++; return nil, nil })
	}
	if calls != 3 {
		t.Errorf("uncached calls = %d, want 3", calls)
	}
}

func TestPipe(t *testing.T) {
	runner := execRunner(t)
	r := testutil.NewRepo(t)
	var lines []string
	err := gitx.Pipe(context.Background(), runner, r.Dir,
		[]string{"rev-list", "--objects", "--all"},
		[]string{"cat-file", "--batch-check=%(objecttype) %(objectname) %(objectsize) %(rest)"},
		func(l string) { lines = append(lines, l) })
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(lines, "\n")
	if len(lines) != 3 || !strings.Contains(joined, "commit ") || !strings.Contains(joined, "blob ") || !strings.Contains(joined, " README.md") {
		t.Errorf("lines: %q", lines)
	}
}

func TestPipeErrors(t *testing.T) {
	runner := execRunner(t)
	r := testutil.NewRepo(t)
	cat := []string{"cat-file", "--batch-check"}
	t.Run("failing producer", func(t *testing.T) {
		err := gitx.Pipe(context.Background(), runner, r.Dir, []string{"rev-list", "--no-such-flag"}, cat, func(string) {})
		var ge *gitx.Error
		if !errors.As(err, &ge) {
			t.Fatalf("want *gitx.Error, got %T %v", err, err)
		}
	})
	t.Run("cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err := gitx.Pipe(ctx, runner, r.Dir, []string{"rev-list", "--objects", "--all"}, cat, func(string) {})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("want context.Canceled, got %v", err)
		}
	})
	t.Run("no git", func(t *testing.T) {
		bad := &gitx.ExecRunner{Path: "git-does-not-exist-brooom"}
		err := gitx.Pipe(context.Background(), bad, r.Dir, []string{"rev-list", "--all"}, cat, func(string) {})
		if !errors.Is(err, gitx.ErrGitNotFound) {
			t.Fatalf("got %v", err)
		}
	})
}
