package gitx_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/gitx"
	"github.com/Tobias-Braun/brooom/internal/testutil"
)

func TestRunInput(t *testing.T) {
	ctx := context.Background()
	t.Run("unsupported runner", func(t *testing.T) {
		fake := fakeRunner(func([]string) (string, error) { return "", nil })
		_, err := gitx.RunInput(ctx, fake, ".", strings.NewReader("x"), "hash-object", "--stdin")
		if !errors.Is(err, gitx.ErrInputUnsupported) {
			t.Fatalf("err = %v, want ErrInputUnsupported", err)
		}
	})
	t.Run("exec runner pipes stdin", func(t *testing.T) {
		r := execRunner(t)
		repo := testutil.NewRepo(t)
		out, err := gitx.RunInput(ctx, r, repo.Dir, strings.NewReader("hello\n"), "hash-object", "--stdin")
		if err != nil {
			t.Fatal(err)
		}
		if want := "ce013625030ba8dba906f756967f9e9ca394464a"; out != want {
			t.Errorf("hash = %q, want %q", out, want)
		}
	})
	t.Run("exec runner returns gitx.Error", func(t *testing.T) {
		r := execRunner(t)
		repo := testutil.NewRepo(t)
		_, err := gitx.RunInput(ctx, r, repo.Dir, strings.NewReader(""), "no-such-command")
		var gerr *gitx.Error
		if !errors.As(err, &gerr) {
			t.Fatalf("err = %v, want *gitx.Error", err)
		}
	})
}

func TestVersion(t *testing.T) {
	tests := []struct {
		in         string
		want       gitx.Version
		wantErr    bool
		atLeast    [2]int
		atLeastRes bool
	}{
		{"git version 2.43.0", gitx.Version{2, 43, 0}, false, [2]int{2, 36}, true},
		{"git version 2.43.0.windows.1", gitx.Version{2, 43, 0}, false, [2]int{2, 44}, false},
		{"git version 2.39.5 (Apple Git-154)", gitx.Version{2, 39, 5}, false, [2]int{2, 39}, true},
		{"git version 2.30", gitx.Version{2, 30, 0}, false, [2]int{2, 31}, false},
		{"git version 3.0.1", gitx.Version{3, 0, 1}, false, [2]int{2, 99}, true},
		{"garbage", gitx.Version{}, true, [2]int{0, 0}, true},
	}
	for _, tc := range tests {
		got, err := gitx.ParseVersion(tc.in)
		if (err != nil) != tc.wantErr {
			t.Fatalf("%q: err = %v", tc.in, err)
		}
		if err != nil {
			continue
		}
		if got != tc.want {
			t.Errorf("%q: got %v, want %v", tc.in, got, tc.want)
		}
		if got.AtLeast(tc.atLeast[0], tc.atLeast[1]) != tc.atLeastRes {
			t.Errorf("%q: AtLeast(%v) != %v", tc.in, tc.atLeast, tc.atLeastRes)
		}
	}
	r := execRunner(t)
	v, err := gitx.GitVersion(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if !v.AtLeast(gitx.MinGitVersion.Major, gitx.MinGitVersion.Minor) {
		t.Skipf("git %s is older than the supported minimum", v)
	}
}
