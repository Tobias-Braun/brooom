package scope

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/testutil"
)

// gitDir marks dir as a repository the cheap way: an empty .git directory.
func gitDir(t testing.TB, dir string) string {
	t.Helper()
	mkdir(t, dir, ".git")
	return dir
}

// gitFile marks dir as a linked worktree or submodule with a .git pointer.
func gitFile(t testing.TB, dir, content string) string {
	t.Helper()
	mkdir(t, dir)
	if err := os.WriteFile(filepath.Join(dir, ".git"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// skipIfInsideRepo skips tests that need a directory with no repository above.
func skipIfInsideRepo(t *testing.T, dir string) {
	t.Helper()
	if root, err := FindRepoRoot(dir); err == nil {
		t.Skipf("temp dir lies inside the repository %s", root)
	}
}

func TestFindRepoRoot(t *testing.T) {
	root := testutil.ResolvedTempDir(t)
	repo := gitDir(t, mkdir(t, root, "repo"))
	deep := mkdir(t, repo, "a", "b", "c")
	file := touch(t, deep, "file.txt")
	worktree := gitFile(t, filepath.Join(root, "wt"), "gitdir: /somewhere/.git/worktrees/wt\n")
	submodule := gitFile(t, filepath.Join(repo, "vendor", "sub"), "gitdir: ../../.git/modules/sub")
	crlf := gitFile(t, filepath.Join(root, "crlf"), "gitdir: x\r\n")
	tests := []struct {
		name  string
		start string
		want  string
	}{
		{"repo root", repo, repo},
		{"nested directory", deep, repo},
		{"file", file, repo},
		{"path with dot components", filepath.Join(deep, ".", "..", "c"), repo},
		{"trailing separator", deep + string(os.PathSeparator), repo},
		{"linked worktree", worktree, worktree},
		{"below linked worktree", mkdir(t, worktree, "src"), worktree},
		{"submodule beats enclosing repo", submodule, submodule},
		{"below submodule", mkdir(t, submodule, "pkg"), submodule},
		{"gitdir file with CRLF", crlf, crlf},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := FindRepoRoot(tc.start)
			if err != nil || got != tc.want {
				t.Fatalf("FindRepoRoot(%q) = %q, %v; want %q", tc.start, got, err, tc.want)
			}
		})
	}
}

func TestFindRepoRootRelativeStart(t *testing.T) {
	root := testutil.ResolvedTempDir(t)
	repo := gitDir(t, mkdir(t, root, "repo"))
	t.Chdir(mkdir(t, repo, "x"))
	for _, start := range []string{".", "..", filepath.Join("..", "x")} {
		if got, err := FindRepoRoot(start); err != nil || got != repo {
			t.Errorf("FindRepoRoot(%q) = %q, %v; want %q", start, got, err, repo)
		}
	}
}

func TestFindRepoRootIgnoresFakeGitFiles(t *testing.T) {
	root := testutil.ResolvedTempDir(t)
	outer := gitDir(t, mkdir(t, root, "outer"))
	tests := []struct {
		name    string
		content string
	}{
		{"arbitrary text", "just some notes\n"},
		{"empty", ""},
		{"gitdir not at the start", "\ngitdir: /x\n"},
		{"wrong case", "GITDIR: /x\n"},
		{"binary", "\x00\x01\x02gitdir: /x"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fake := gitFile(t, filepath.Join(outer, "fake-"+strings.ReplaceAll(tc.name, " ", "-")), tc.content)
			got, err := FindRepoRoot(fake)
			if err != nil || got != outer {
				t.Fatalf("FindRepoRoot(%q) = %q, %v; the fake .git must be skipped and %q found", fake, got, err, outer)
			}
		})
	}
}

func TestFindRepoRootReadsAtMostFourKiB(t *testing.T) {
	root := testutil.ResolvedTempDir(t)
	outer := gitDir(t, mkdir(t, root, "outer"))
	// The pointer only appears after the probe window, so it must not count.
	late := gitFile(t, filepath.Join(outer, "late"), strings.Repeat("x", gitFileProbeSize)+"gitdir: /x\n")
	if got, err := FindRepoRoot(late); err != nil || got != outer {
		t.Fatalf("FindRepoRoot = %q, %v; want %q", got, err, outer)
	}
}

func TestFindRepoRootNoRepo(t *testing.T) {
	dir := mkdir(t, testutil.ResolvedTempDir(t), "plain", "sub")
	skipIfInsideRepo(t, dir)
	_, err := FindRepoRoot(dir)
	if !errors.Is(err, ErrNotInRepo) {
		t.Fatalf("err = %v, want ErrNotInRepo", err)
	}
	if !strings.Contains(err.Error(), dir) {
		t.Errorf("error %q should name the start path %q", err, dir)
	}
}

func TestFindRepoRootBareRepositoryIsNotARepo(t *testing.T) {
	root := testutil.ResolvedTempDir(t)
	bare := mkdir(t, root, "bare.git")
	mkdir(t, bare, "objects")
	mkdir(t, bare, "refs")
	touch(t, bare, "HEAD")
	skipIfInsideRepo(t, bare)
	if got, err := FindRepoRoot(bare); !errors.Is(err, ErrNotInRepo) {
		t.Fatalf("FindRepoRoot(bare) = %q, %v; want ErrNotInRepo", got, err)
	}
}

func TestFindRepoRootMissingStart(t *testing.T) {
	missing := filepath.Join(testutil.ResolvedTempDir(t), "missing", "deeper")
	_, err := FindRepoRoot(missing)
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("err = %v, want the underlying fs error", err)
	}
	if errors.Is(err, ErrNotInRepo) {
		t.Errorf("a missing start must not be reported as ErrNotInRepo: %v", err)
	}
}

func TestFindRepoRootInvalidStart(t *testing.T) {
	for _, start := range []string{"", "a\x00b"} {
		if got, err := FindRepoRoot(start); err == nil {
			t.Errorf("FindRepoRoot(%q) = %q, want error", start, got)
		}
	}
}

func TestFindRepoRootStopsAtFilesystemRoot(t *testing.T) {
	root := volumeRoot(t)
	// Either there is no repository at the root (the walk terminates with
	// ErrNotInRepo) or the machine keeps one there; both are fine, hanging is
	// not.
	if _, err := FindRepoRoot(root); err != nil && !errors.Is(err, ErrNotInRepo) {
		t.Fatalf("FindRepoRoot(%q) = %v, want nil or ErrNotInRepo", root, err)
	}
}

func TestFindRepoRootRealRepository(t *testing.T) {
	r := testutil.NewRepo(t)
	sub := mkdir(t, r.Dir, "pkg", "x")
	if got, err := FindRepoRoot(sub); err != nil || got != r.Dir {
		t.Fatalf("FindRepoRoot = %q, %v; want %q", got, err, r.Dir)
	}
	r.Git("branch", "feature")
	wt := filepath.Join(testutil.ResolvedTempDir(t), "wt")
	r.Git("worktree", "add", "-q", wt, "feature")
	wt, err := filepath.EvalSymlinks(wt)
	if err != nil {
		t.Fatal(err)
	}
	inWt := mkdir(t, wt, "inner")
	if got, err := FindRepoRoot(inWt); err != nil || got != wt {
		t.Fatalf("FindRepoRoot(worktree) = %q, %v; want %q", got, err, wt)
	}
}

func TestFindRepoRootSymlinks(t *testing.T) {
	requireSymlink(t)
	root := testutil.ResolvedTempDir(t)
	repo := gitDir(t, mkdir(t, root, "repo"))
	real := mkdir(t, repo, "pkg")
	cwdLink := filepath.Join(root, "cwd-link")
	symlink(t, real, cwdLink)
	t.Run("symlinked start returns the real path", func(t *testing.T) {
		if got, err := FindRepoRoot(cwdLink); err != nil || got != repo {
			t.Fatalf("FindRepoRoot(%q) = %q, %v; want %q", cwdLink, got, err, repo)
		}
	})
	t.Run("symlinked working directory", func(t *testing.T) {
		t.Chdir(cwdLink)
		if got, err := FindRepoRoot("."); err != nil || got != repo {
			t.Fatalf("FindRepoRoot(.) = %q, %v; want %q", got, err, repo)
		}
	})
	t.Run(".git as symlink to a directory", func(t *testing.T) {
		store := mkdir(t, root, "store", "gitdata")
		linked := mkdir(t, root, "linked")
		symlink(t, store, filepath.Join(linked, ".git"))
		if got, err := FindRepoRoot(mkdir(t, linked, "sub")); err != nil || got != linked {
			t.Fatalf("FindRepoRoot = %q, %v; want %q", got, err, linked)
		}
	})
	t.Run("dangling .git symlink is skipped", func(t *testing.T) {
		outer := gitDir(t, mkdir(t, root, "outer2"))
		dangling := mkdir(t, outer, "dangling")
		symlink(t, filepath.Join(root, "nowhere"), filepath.Join(dangling, ".git"))
		if got, err := FindRepoRoot(dangling); err != nil || got != outer {
			t.Fatalf("FindRepoRoot = %q, %v; want %q", got, err, outer)
		}
	})
}
