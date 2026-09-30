package gitx_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/gitx"
)

// stderrRunner fails every command like git does for a fatal error: exit 128
// with the given stderr.
type stderrRunner struct{ stderr string }

func (r stderrRunner) Run(_ context.Context, dir string, args ...string) (string, error) {
	return "", &gitx.Error{Args: args, Dir: dir, ExitCode: 128, Stderr: r.stderr}
}

// TestOpenClassifiesGitFailures pins that only "not a git repository" is
// ErrNotRepo: dubious ownership must stay visible and other failures keep
// git's stderr instead of being reported as "not a repository".
func TestOpenClassifiesGitFailures(t *testing.T) {
	dubious := "fatal: detected dubious ownership in repository at '/w/repo'\n" +
		"To add an exception for this directory, call:\n\n\tgit config --global --add safe.directory /w/repo\n"
	tests := []struct {
		name       string
		stderr     string
		wantNot    bool
		wantUnsafe bool
		wantText   string
	}{
		{"not a repo", "fatal: not a git repository (or any of the parent directories): .git\n", true, false, ""},
		{"dubious ownership", dubious, false, true, "git config --global --add safe.directory /w/repo"},
		{"other failure", "fatal: unable to read config file '/x': Permission denied\n", false, false, "Permission denied"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := gitx.Open(context.Background(), stderrRunner{tc.stderr}, "/w/repo")
			if err == nil {
				t.Fatal("expected an error")
			}
			if got := errors.Is(err, gitx.ErrNotRepo); got != tc.wantNot {
				t.Errorf("Is(ErrNotRepo) = %v, want %v (err %v)", got, tc.wantNot, err)
			}
			if got := errors.Is(err, gitx.ErrUnsafeRepo); got != tc.wantUnsafe {
				t.Errorf("Is(ErrUnsafeRepo) = %v, want %v (err %v)", got, tc.wantUnsafe, err)
			}
			if tc.wantText != "" && !strings.Contains(err.Error(), tc.wantText) {
				t.Errorf("err = %q, want it to contain %q", err, tc.wantText)
			}
		})
	}
}

func TestEnvForcesCLocale(t *testing.T) {
	env := gitx.Env([]string{"LC_ALL=de_DE.UTF-8"})
	if env[len(env)-2] != "LC_ALL=C" {
		t.Fatalf("LC_ALL=C must come after the inherited value: %v", env)
	}
}
