package action

import (
	"context"
	"strings"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/gitx"
	"github.com/Tobias-Braun/brooom/internal/scope"
	"github.com/Tobias-Braun/brooom/internal/testutil"
)

// dubiousRunner makes every git call fail like git does in a repository owned
// by another user.
type dubiousRunner struct{}

func (dubiousRunner) Run(_ context.Context, dir string, args ...string) (string, error) {
	return "", &gitx.Error{Args: args, Dir: dir, ExitCode: 128, Stderr: "fatal: detected dubious ownership in repository at '" + dir + "'\n"}
}

// TestActionsReportDubiousOwnershipAsSkip pins that a repository git refuses is
// a visible, explained skip and never "not a git repository".
func TestActionsReportDubiousOwnershipAsSkip(t *testing.T) {
	dir := testutil.ResolvedTempDir(t)
	guard, err := scope.NewGuard(dir)
	if err != nil {
		t.Fatal(err)
	}
	env := &Env{Config: config.Default(), Git: dubiousRunner{}, Guard: guard}
	ctx := context.Background()

	t.Run("worktree repository", func(t *testing.T) {
		_, err := openRepoDir(ctx, env, dir)
		wantBranchSkip(t, err, "dubious ownership")
		if strings.Contains(err.Error(), "not a usable") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("delete-branch", func(t *testing.T) {
		f := findings.Finding{Path: dir, Kind: findings.KindBranch, Ref: "feat/x"}
		_, err := openRepo(ctx, env, f)
		wantBranchSkip(t, err, "git config --global --add safe.directory")
		if strings.Contains(err.Error(), "is not a git repository") {
			t.Fatalf("err = %v", err)
		}
	})
}
