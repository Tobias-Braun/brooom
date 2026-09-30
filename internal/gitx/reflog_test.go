package gitx

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Tobias-Braun/brooom/internal/testutil"
)

func TestReflogExpireArgsProtectStashAndKeepDateAValue(t *testing.T) {
	args := ReflogExpireArgs("90.days.ago", true)
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"-c gc.refs/stash.reflogExpire=never", "-c gc.refs/stash.reflogExpireUnreachable=never",
		"-c gc.reflogExpire=90.days.ago", "reflog expire --dry-run --verbose --all",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("args %q lack %q", joined, want)
		}
	}
	if slices.Contains(args, "--expire=90.days.ago") {
		t.Error("--expire on the command line would override the per-ref stash protection")
	}
	if strings.Contains(strings.Join(ReflogExpireArgs("x", false), " "), "--dry-run") {
		t.Error("the real run must not be a dry run")
	}
	if !strings.HasPrefix(ReflogExpireCommand("now"), "git -c ") {
		t.Errorf("command = %q", ReflogExpireCommand("now"))
	}
}

func TestStashExpiring(t *testing.T) {
	runner, err := NewExecRunner()
	if err != nil {
		t.Skipf("git not available: %v", err)
	}
	repo := testutil.NewRepo(t)
	ctx := context.Background()
	if n, err := StashExpiring(ctx, runner, repo.Dir, "now"); err != nil || n != 0 {
		t.Fatalf("no stash: n=%d err=%v", n, err)
	}
	old := time.Date(2001, 1, 1, 12, 0, 0, 0, time.UTC)
	repo.WriteFile("a.txt", "base")
	repo.CommitAll("track", old)
	repo.WriteFile("a.txt", "work")
	repo.GitAt(old, "stash", "push", "-q", "-m", "old")
	for date, want := range map[string]int{"2010-01-01": 1, "1990-01-01": 0} {
		if n, err := StashExpiring(ctx, runner, repo.Dir, date); err != nil || n != want {
			t.Errorf("date %s: n=%d err=%v, want %d", date, n, err, want)
		}
	}
	// git's own defaults spare the stash reflog, so nothing would expire.
	if n, err := StashExpiring(ctx, runner, repo.Dir, ""); err != nil || n != 0 {
		t.Errorf("default settings: n=%d err=%v, want 0", n, err)
	}
}
