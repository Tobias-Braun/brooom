package action

import (
	"context"
	"errors"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/gitx"
)

// TestMaintCommandsQuoteRepoDirAndDate: the Command of a maintenance step is
// pasted into a shell, so a repository path with spaces or metacharacters and
// an approxidate such as "2 weeks ago" (accepted by ValidateDate) must each
// stay one word. They used to be joined raw and split into several.
func TestMaintCommandsQuoteRepoDirAndDate(t *testing.T) {
	const date = "2 weeks ago"
	dir := filepath.Join(string(filepath.Separator)+"work", "my repo;x")
	tests := []struct {
		name string
		args []string
		// word is the option token that must appear quoted as one word.
		word string
	}{
		{"gc", gcArgs(date), "--prune=" + date},
		{"prune", []string{"prune", "--expire=" + date}, "--expire=" + date},
		{"reflog", gitx.ReflogExpireArgs(date, false), "gc.reflogExpire=" + date},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := maintCommand(dir, tc.args)
			if !strings.HasPrefix(got, "git -C "+findings.Quote(dir)+" ") {
				t.Errorf("command %q does not start with the quoted repository dir", got)
			}
			if !strings.Contains(got, findings.Quote(tc.word)) || strings.Contains(got, " "+tc.word) {
				t.Errorf("command %q lacks the quoted word %q", got, findings.Quote(tc.word))
			}
			if runtime.GOOS != "windows" && !strings.Contains(got, "'"+dir+"'") {
				t.Errorf("command %q: dir must be single-quoted on POSIX", got)
			}
		})
	}
}

// TestMaintPlanCommandQuotesApproxidate runs the real Plan of all three
// maintenance actions with a date that holds spaces.
func TestMaintPlanCommandQuotesApproxidate(t *testing.T) {
	const date = "2 weeks ago"
	fx := newMaintFixture(t)
	fx.looseCommits(3)
	fx.dangling("only reachable through nothing")
	tests := []struct {
		typ  findings.ActionType
		arg  string
		word string
	}{
		{findings.ActionGitGC, "prune", "--prune=" + date},
		{findings.ActionGitPrune, "expire", "--expire=" + date},
		{findings.ActionGitReflogExpire, "expire", "gc.reflogExpire=" + date},
	}
	for _, tc := range tests {
		t.Run(string(tc.typ), func(t *testing.T) {
			f := fx.finding(tc.typ, map[string]string{tc.arg: date})
			step, err := act(t, tc.typ).Plan(context.Background(), fx.env, f)
			if errors.Is(err, ErrSkipped) {
				t.Skipf("nothing to plan for %s on this fixture: %v", tc.typ, err)
			}
			if err != nil {
				t.Fatal(err)
			}
			if want := findings.Quote(tc.word); !strings.Contains(step.Command, want) {
				t.Errorf("command = %q, want the quoted word %s", step.Command, want)
			}
			if want := "git -C " + findings.Quote(fx.repo.Dir) + " "; !strings.HasPrefix(step.Command, want) {
				t.Errorf("command = %q, want prefix %q", step.Command, want)
			}
		})
	}
}
