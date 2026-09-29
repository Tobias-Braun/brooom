package gitx_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/gitx"
	"github.com/Tobias-Braun/brooom/internal/testutil"
)

func TestExecRunner(t *testing.T) {
	repo := testutil.NewRepo(t)
	r, err := gitx.NewExecRunner()
	if err != nil {
		t.Skip("git not installed:", err)
	}
	out, err := r.Run(context.Background(), repo.Dir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if out != "main" {
		t.Errorf("branch = %q, want main", out)
	}
	_, err = r.Run(context.Background(), repo.Dir, "rev-parse", "--verify", "does-not-exist")
	var gerr *gitx.Error
	if !errors.As(err, &gerr) || gerr.ExitCode == 0 {
		t.Fatalf("expected *gitx.Error with non-zero exit, got %v", err)
	}
}

func TestLines(t *testing.T) {
	got := gitx.Lines("a\r\nb\n\nc")
	if len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Errorf("Lines = %q", got)
	}
	if gitx.Lines("") != nil {
		t.Error("Lines(\"\") must be nil")
	}
}
