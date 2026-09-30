package procs

import (
	"context"
	"errors"
	"os/exec"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestParseLsof(t *testing.T) {
	tests := []struct {
		name string
		out  string
		want []string
	}{
		{"empty", "", nil},
		{"single record", "p123\x00\nf4\x00n/tmp/a.log\x00\n", []string{"/tmp/a.log"}},
		{"spaces", "p1\x00\nf3\x00n/tmp/with space.log\x00\n", []string{"/tmp/with space.log"}},
		{"newline in name", "p1\x00\nf3\x00n/tmp/new\nline.log\x00\n", []string{"/tmp/new\nline.log"}},
		{"name starting with n and digits", "p1\x00\nf3\x00n/tmp/n1\x00\n", []string{"/tmp/n1"}},
		{
			"several processes",
			"p1\x00\nf3\x00n/a\x00\nf4\x00n/b\x00\np2\x00\nf5\x00n/c\x00\n",
			[]string{"/a", "/b", "/c"},
		},
		{"no name field", "p1\x00\nf3\x00\n", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseLsof([]byte(tt.out)); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBatchPaths(t *testing.T) {
	var many []string
	for i := 0; i < 250; i++ {
		many = append(many, "/p/"+strconv.Itoa(i))
	}
	long := strings.Repeat("x", 40)
	tests := []struct {
		name     string
		paths    []string
		maxN     int
		maxBytes int
		sizes    []int
	}{
		{"none", nil, 100, 1000, nil},
		{"count limit", many, 100, 1 << 20, []int{100, 100, 50}},
		{"byte limit", []string{long, long, long}, 100, 90, []int{2, 1}},
		{"oversized single", []string{long}, 100, 5, []int{1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var sizes []int
			total := 0
			for _, b := range batchPaths(tt.paths, tt.maxN, tt.maxBytes) {
				sizes = append(sizes, len(b))
				total += len(b)
			}
			if !reflect.DeepEqual(sizes, tt.sizes) || total != len(tt.paths) {
				t.Errorf("sizes %v (total %d), want %v", sizes, total, tt.sizes)
			}
		})
	}
}

// fakeLsof answers from a table keyed by the trailing argument (the file
// batch is joined with "|"). It records the calls.
type fakeLsof struct {
	calls [][]string
	reply func(args []string) ([]byte, error)
}

func (f *fakeLsof) run(_ context.Context, args []string) ([]byte, error) {
	f.calls = append(f.calls, args)
	return f.reply(args)
}

func exitError(t *testing.T, code int) error {
	t.Helper()
	// A real *exec.ExitError cannot be constructed directly, so run a
	// trivial program that exits with the wanted code. Skipped where no
	// shell is available.
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh to fabricate an exit status")
	}
	err = exec.Command(sh, "-c", "exit "+strconv.Itoa(code)).Run()
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		t.Skipf("cannot fabricate exit error: %v", err)
	}
	return err
}

func TestLsofOpenFilesMapsNames(t *testing.T) {
	files := []string{"/tmp/Open.log", "/tmp/closed.log", "/tmp/exact.log"}
	dirs := []string{"/tmp/busy", "/tmp/idle"}
	f := &fakeLsof{reply: func(args []string) ([]byte, error) {
		if args[len(args)-2] == "+D" {
			if args[len(args)-1] == "/tmp/busy" {
				return []byte("p1\x00\nf3\x00n/TMP/busy/x.log\x00\n"), nil
			}
			return nil, nil
		}
		// lsof spells the first file in another case (case-insensitive FS).
		return []byte("p1\x00\nf3\x00n/tmp/open.log\x00\nf4\x00n/tmp/exact.log\x00\n"), exitErrorNoSkip()
	}}
	res := map[string]bool{}
	for _, p := range append(append([]string{}, files...), dirs...) {
		res[p] = false
	}
	if err := lsofOpenFiles(context.Background(), f.run, files, dirs, res); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := map[string]bool{
		"/tmp/Open.log": true, "/tmp/closed.log": false, "/tmp/exact.log": true,
		"/tmp/busy": true, "/tmp/idle": false,
	}
	if !reflect.DeepEqual(res, want) {
		t.Errorf("got %v, want %v", res, want)
	}
	if got := f.calls[0][len(f.calls[0])-len(files)-1]; got != "--" {
		t.Errorf("paths must follow --, got args %q", f.calls[0])
	}
}

// exitErrorNoSkip returns a non-nil error standing in for lsof's exit 1 when
// some arguments matched nothing; output is non-empty so its type is moot.
func exitErrorNoSkip() error { return errors.New("exit status 1") }

func TestLsofOpenFilesExitStatuses(t *testing.T) {
	tests := []struct {
		name    string
		out     string
		err     func(t *testing.T) error
		wantErr error
		want    bool
	}{
		{"exit 1 empty means none open", "", func(t *testing.T) error { return exitError(t, 1) }, nil, false},
		{"exit 0 with output", "p1\x00\nf3\x00n/a/f\x00\n", func(*testing.T) error { return nil }, nil, true},
		{"other failure without output", "", func(t *testing.T) error { return exitError(t, 2) }, ErrUnavailable, false},
		{"start failure", "", func(*testing.T) error { return errors.New("boom") }, ErrUnavailable, false},
		{"unavailable passes through", "", func(*testing.T) error { return ErrUnavailable }, ErrUnavailable, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runErr := tt.err(t)
			run := func(context.Context, []string) ([]byte, error) { return []byte(tt.out), runErr }
			res := map[string]bool{"/a/f": false}
			err := lsofOpenFiles(context.Background(), run, []string{"/a/f"}, nil, res)
			if !errors.Is(err, tt.wantErr) || (tt.wantErr == nil && err != nil) {
				t.Errorf("err = %v, want %v", err, tt.wantErr)
			}
			if res["/a/f"] != tt.want {
				t.Errorf("got %v, want %v", res["/a/f"], tt.want)
			}
		})
	}
}

func TestLsofDirectoryTimeoutIsIncompleteButContinues(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	run := func(c context.Context, args []string) ([]byte, error) {
		if args[len(args)-1] == "/slow" {
			<-c.Done() // the per-directory slice must expire
			return nil, c.Err()
		}
		return []byte("p1\x00\nf3\x00n/fast/x\x00\n"), nil
	}
	res := map[string]bool{"/slow": false, "/fast": false}
	start := time.Now()
	err := lsofOpenFiles(ctx, run, nil, []string{"/slow", "/fast"}, res)
	if !errors.Is(err, ErrIncomplete) {
		t.Fatalf("got %v, want ErrIncomplete", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Errorf("slice did not bound the slow directory: %v", time.Since(start))
	}
	if !res["/fast"] || res["/slow"] {
		t.Errorf("unexpected result %v", res)
	}
}

func TestLsofCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	run := func(c context.Context, _ []string) ([]byte, error) { return nil, c.Err() }
	res := map[string]bool{"/a": false}
	err := lsofOpenFiles(ctx, run, []string{"/a"}, nil, res)
	if !errors.Is(err, ErrIncomplete) {
		t.Errorf("got %v, want ErrIncomplete", err)
	}
}

func TestAnyBelowIgnoresDirItselfAndLookalikes(t *testing.T) {
	prefix := "/tmp/dir/"
	tests := []struct {
		names []string
		want  bool
	}{
		{nil, false},
		{[]string{"/tmp/dir"}, false},
		{[]string{"/tmp/dir/"}, false},
		{[]string{"/tmp/dirx/file"}, false},
		{[]string{"/tmp/dir/file"}, true},
		{[]string{"/TMP/DIR/file"}, true},
	}
	for _, tt := range tests {
		if got := anyBelow(tt.names, prefix); got != tt.want {
			t.Errorf("anyBelow(%q) = %v, want %v", tt.names, got, tt.want)
		}
	}
}
