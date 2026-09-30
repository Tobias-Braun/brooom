package gitx_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/gitx"
	"github.com/Tobias-Braun/brooom/internal/testutil"
)

// recordingRunner remembers every call and answers from fn.
type recordingRunner struct {
	mu    sync.Mutex
	calls [][]string
	fn    func(args []string) (string, error)
	// config is the answer to the core.ignorecase lookup.
	config    string
	configErr error
}

// lsCalls returns the recorded ls-files calls, leaving the config lookup out.
func (r *recordingRunner) lsCalls() [][]string {
	var out [][]string
	for _, c := range r.calls {
		if c[0] == "ls-files" {
			out = append(out, c)
		}
	}
	return out
}

func (r *recordingRunner) Run(_ context.Context, _ string, args ...string) (string, error) {
	r.mu.Lock()
	r.calls = append(r.calls, append([]string(nil), args...))
	r.mu.Unlock()
	if args[0] == "config" {
		return r.config, r.configErr
	}
	return r.fn(args)
}

func TestTrackedUnderMatchesFilesAndDirectories(t *testing.T) {
	out := "src/a.go\x00logs/keep/x.log\x00.DS_Store\x00"
	rr := &recordingRunner{fn: func([]string) (string, error) { return out, nil }}
	rels := []string{"src", "src/a.go", "src/b.go", "logs", "logs/other", ".DS_Store", "sub/.DS_Store", "."}
	got, err := gitx.TrackedUnder(context.Background(), rr, "/repo", rels)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"src": true, "src/a.go": true, "src/b.go": false, "logs": true,
		"logs/other": false, ".DS_Store": true, "sub/.DS_Store": false, ".": true,
	}
	for rel, w := range want {
		if got[rel] != w {
			t.Errorf("tracked[%q] = %v, want %v", rel, got[rel], w)
		}
	}
	if n := len(rr.lsCalls()); n != 1 {
		t.Errorf("ls-files calls = %d, want 1", n)
	}
}

func TestTrackedUnderPrefixIsNotAncestor(t *testing.T) {
	rr := &recordingRunner{fn: func([]string) (string, error) { return "logsfile/x\x00", nil }}
	got, err := gitx.TrackedUnder(context.Background(), rr, "/repo", []string{"logs"})
	if err != nil || got["logs"] {
		t.Fatalf("got %v, %v: a sibling with a common prefix must not count", got, err)
	}
}

// TestTrackedUnderCaseInsensitiveErrsOnTheSafeSide: git matches pathspecs
// ignoring case on case-insensitive filesystems, so a case-only difference
// is reported as tracked (blocking) rather than missed.
func TestTrackedUnderCaseInsensitiveErrsOnTheSafeSide(t *testing.T) {
	rr := &recordingRunner{config: "true\n", fn: func([]string) (string, error) { return "Docs/README.md\x00", nil }}
	got, err := gitx.TrackedUnder(context.Background(), rr, "/repo", []string{"docs"})
	if err != nil || !got["docs"] {
		t.Fatalf("got %v, %v, want docs reported tracked", got, err)
	}
	rr.configErr = errors.New("no config")
	got, err = gitx.TrackedUnder(context.Background(), rr, "/repo", []string{"docs"})
	if err != nil || !got["docs"] {
		t.Fatalf("unreadable core.ignorecase: got %v, %v, want the safe side (tracked)", got, err)
	}
}

// TestTrackedUnderCaseSensitiveComparesExactly: with core.ignorecase false a
// tracked Logs/x.log must not block the distinct untracked logs directory.
func TestTrackedUnderCaseSensitiveComparesExactly(t *testing.T) {
	rr := &recordingRunner{config: "false\n", fn: func([]string) (string, error) { return "Logs/x.log\x00", nil }}
	got, err := gitx.TrackedUnder(context.Background(), rr, "/repo", []string{"logs", "Logs"})
	if err != nil || got["logs"] || !got["Logs"] {
		t.Fatalf("got %v, %v, want only Logs tracked", got, err)
	}
}

// TestTrackedUnderNonASCIIAsksGitPerPath: a candidate in NFD (as macOS finds
// it on disk) must be matched against the NFC index entry by git itself,
// which normalises the pathspec; a byte-wise match in Go would report it
// untracked.
func TestTrackedUnderNonASCIIAsksGitPerPath(t *testing.T) {
	const nfd = "caf\u0065\u0301/x.log"
	const nfc = "caf\u00e9/x.log"
	rr := &recordingRunner{config: "false\n", fn: func(args []string) (string, error) {
		for _, a := range args[3:] {
			if a == ":(literal)"+nfd {
				return nfc + "\x00", nil
			}
		}
		return "", nil
	}}
	got, err := gitx.TrackedUnder(context.Background(), rr, "/repo", []string{nfd, "plain/y.log"})
	if err != nil {
		t.Fatal(err)
	}
	if !got[nfd] || got["plain/y.log"] {
		t.Fatalf("got %v, want the NFD candidate tracked and the plain one not", got)
	}
	if n := len(rr.lsCalls()); n != 2 {
		t.Errorf("ls-files calls = %d, want 2 (one per non-ASCII candidate, one batch)", n)
	}
}

// TestTrackedChunksLimits pins both limits of the chunking.
func TestTrackedChunksLimits(t *testing.T) {
	var many []string
	for i := 0; i < 1000; i++ {
		many = append(many, fmt.Sprintf("d%d", i))
	}
	if n := len(gitx.TrackedChunks(many)); n != 3 {
		t.Errorf("chunks by count = %d, want 3 (400+400+200)", n)
	}
	long := strings.Repeat("a", 5000)
	var big []string
	for i := 0; i < 10; i++ {
		big = append(big, fmt.Sprintf("%s%d", long, i))
	}
	chunks := gitx.TrackedChunks(big)
	if len(chunks) != 4 {
		t.Fatalf("chunks by bytes = %d, want 4 (3+3+3+1)", len(chunks))
	}
	for _, c := range chunks {
		if len(c) > 3 {
			t.Errorf("chunk of %d long paths exceeds the byte budget", len(c))
		}
	}
}

func TestTrackedUnderChunksAndUsesLiteralPathspecs(t *testing.T) {
	rr := &recordingRunner{fn: func([]string) (string, error) { return "", nil }}
	var rels []string
	for i := 0; i < 1000; i++ {
		rels = append(rels, fmt.Sprintf("d%d/*.log", i))
	}
	if _, err := gitx.TrackedUnder(context.Background(), rr, "/repo", rels); err != nil {
		t.Fatal(err)
	}
	if n := len(rr.lsCalls()); n != 3 {
		t.Errorf("ls-files calls = %d for 1000 candidates, want 3 chunks", n)
	}
	total := 0
	for _, c := range rr.lsCalls() {
		total += len(c) - 3
		for _, a := range c[3:] {
			if !strings.HasPrefix(a, ":(literal)") {
				t.Fatalf("pathspec %q is not literal", a)
			}
		}
	}
	if total != len(rels) {
		t.Errorf("pathspecs sent = %d, want %d", total, len(rels))
	}
}

func TestTrackedUnderFailClosed(t *testing.T) {
	boom := errors.New("boom")
	rr := &recordingRunner{fn: func([]string) (string, error) { return "", boom }}
	got, err := gitx.TrackedUnder(context.Background(), rr, "/repo", []string{"a", "b"})
	if !errors.Is(err, boom) || got != nil {
		t.Fatalf("got %v, %v, want the error and no map", got, err)
	}
}

func TestTrackedUnderEmptyRunsNothing(t *testing.T) {
	rr := &recordingRunner{fn: func([]string) (string, error) { return "", nil }}
	if _, err := gitx.TrackedUnder(context.Background(), rr, "/repo", nil); err != nil {
		t.Fatal(err)
	}
	if len(rr.calls) != 0 {
		t.Errorf("git calls = %d, want 0", len(rr.calls))
	}
}

// TestTrackedUnderAgainstRealGit compares the batch with one ls-files per path.
func TestTrackedUnderAgainstRealGit(t *testing.T) {
	r := execRunner(t)
	repo := testutil.NewRepo(t)
	repo.WriteFile("src/a.go", "x")
	repo.WriteFile("logs/tracked.log", "x")
	repo.WriteFile("[weird]/f.txt", "x")
	repo.CommitAll("init", at(0))
	repo.WriteFile("logs/untracked.log", "x")
	repo.WriteFile("other/u.log", "x")
	rels := []string{"src", "src/a.go", "logs", "logs/tracked.log", "logs/untracked.log", "other", "other/u.log", "[weird]", "*", "."}
	got, err := gitx.TrackedUnder(context.Background(), r, repo.Dir, rels)
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range rels {
		out, err := r.Run(context.Background(), repo.Dir, "ls-files", "-z", "--", ":(literal)"+rel)
		if err != nil {
			t.Fatal(err)
		}
		if want := out != ""; got[rel] != want {
			t.Errorf("tracked[%q] = %v, single ls-files says %v", rel, got[rel], want)
		}
	}
}
