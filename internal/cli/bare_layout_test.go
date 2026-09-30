package cli

import (
	"context"
	"errors"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/gitx"
	"github.com/Tobias-Braun/brooom/internal/testutil"
)

// TestRepoTargetsBareAnchor is the CLI half of issue #200: running inside the
// bare project folder is a visible usage error instead of silent 0 findings,
// and running inside one of its worktrees registers the bare directory as
// repository metadata location so the branch detectors can run git there.
func TestRepoTargetsBareAnchor(t *testing.T) {
	needGit(t)
	l := testutil.NewBareLayout(t)
	runner, err := gitx.NewExecRunner()
	if err != nil {
		t.Skip("git not installed:", err)
	}

	t.Run("anchor folder", func(t *testing.T) {
		t.Chdir(l.Root)
		_, err := repoTargets(context.Background(), runner)
		var ue usageError
		if !errors.As(err, &ue) || !errors.Is(err, errBareAnchor) {
			t.Fatalf("err = %v, want the bare anchor usage error", err)
		}
	})

	t.Run("linked worktree", func(t *testing.T) {
		t.Chdir(l.Feat)
		ts, err := repoTargets(context.Background(), runner)
		if err != nil {
			t.Fatal(err)
		}
		if len(ts.errs) != 0 || len(ts.repoMeta) != 1 || ts.repoMeta[0] != l.Bare {
			t.Errorf("errs %+v repoMeta %v, want the bare anchor %q", ts.errs, ts.repoMeta, l.Bare)
		}
	})
}
