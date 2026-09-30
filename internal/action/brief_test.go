package action

import (
	"bytes"
	"strings"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/session"
)

func applied(action findings.ActionType, detector string) session.Entry {
	return session.Entry{Action: action, Detector: detector, Status: session.StatusApplied}
}

func TestBriefCounts(t *testing.T) {
	tests := []struct {
		name    string
		entries []session.Entry
		want    string
	}{
		{"nothing", nil, ""},
		{"singular", []session.Entry{applied(findings.ActionRemoveWorktree, config.DetectorWorktrees)}, "1 worktree deleted"},
		{
			"plural in table order",
			[]session.Entry{
				applied(findings.ActionDeleteBranch, config.DetectorStaleBranch),
				applied(findings.ActionRemoveWorktree, config.DetectorWorktrees),
				applied(findings.ActionDeleteBranch, config.DetectorStaleBranch),
				applied(findings.ActionRemoveWorktree, config.DetectorWorktrees),
			},
			"2 worktrees deleted, 2 stale branches removed",
		},
		{
			"git maintenance actions share one count",
			[]session.Entry{applied(findings.ActionGitGC, config.DetectorGitBloat), applied(findings.ActionGitPrune, config.DetectorGitBloat)},
			"2 git maintenance steps completed",
		},
		{"unknown detector falls back", []session.Entry{applied(findings.ActionTrash, "brand-new")}, "1 item removed"},
		{
			"failed and skipped entries are not counted",
			[]session.Entry{
				{Action: findings.ActionRemoveWorktree, Status: session.StatusFailed},
				{Action: findings.ActionRemoveWorktree, Status: session.StatusSkipped},
			},
			"",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := briefCounts(tt.entries); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRenderBriefSummary(t *testing.T) {
	res := &Result{
		SessionID:      "20260930-101500-abcd",
		Entries:        []session.Entry{applied(findings.ActionRemoveWorktree, config.DetectorWorktrees), applied(findings.ActionDeleteBranch, config.DetectorStaleBranch)},
		ReclaimedBytes: 3_000_000,
	}
	res.Entries[0].Restorable = true
	res.Applied = 2

	var out bytes.Buffer
	renderBriefSummary(&out, res, false)
	want := "undo: brooom undo 20260930-101500-abcd\n" +
		"1 worktree deleted, 1 stale branch removed. 3.0 MB reclaimed\n"
	if out.String() != want {
		t.Errorf("got %q, want %q", out.String(), want)
	}

	res.Skipped = 2
	res.Failures = []session.Entry{{Path: "/r/wt", Status: session.StatusFailed, Error: "in use"}}
	out.Reset()
	renderBriefSummary(&out, res, false)
	for _, want := range []string{"failures (1):\n  /r/wt: in use\n", "2 items skipped", "reclaimed\n"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
	if !strings.HasSuffix(out.String(), " reclaimed\n") {
		t.Errorf("reclaimed size is not the last line:\n%s", out.String())
	}

	out.Reset()
	renderBriefSummary(&out, res, true)
	if want := "failures (1):\n  /r/wt: in use\n"; out.String() != want {
		t.Errorf("quiet: got %q, want %q", out.String(), want)
	}
}

func TestRenderBriefSummaryNothingApplied(t *testing.T) {
	var out bytes.Buffer
	renderBriefSummary(&out, &Result{Skipped: 1}, false)
	if want := "1 item skipped (blocked or changed since the scan; run with --verbose for details)\nnothing cleaned. 0 B reclaimed\n"; out.String() != want {
		t.Errorf("got %q", out.String())
	}
}
