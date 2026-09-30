package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestOutsideScopeWorktreeIsShownWithoutVerbose is the regression test for a
// linked worktree at ../repo-wt: a plain run must mention it with the hint
// instead of printing "nothing to clean", in every format, and must never
// offer it for removal even when its branch is merged.
func TestOutsideScopeWorktreeIsShownWithoutVerbose(t *testing.T) {
	f := newCleanupFixture(t, nil)
	f.feature("feat/outside")
	f.mergeCommit("feat/outside")
	f.publish()
	outside := f.repo.AddWorktree("outside", "feat/outside")
	t.Chdir(f.repo.Dir)

	name := filepath.Base(outside)
	// Every human format names the worktree and carries the hint; the machine
	// formats additionally carry the evidence code. Summary only counts, and
	// plain is an xargs-style path pipe that lists actionable findings only, so
	// it must stay silent about the worktree.
	formats := map[string][]string{
		"table":   {name, "roots add"},
		"tree":    {name, "roots add"},
		"plain":   nil,
		"summary": nil,
		"json":    {name, "roots add", "outside_scope"},
		"ndjson":  {name, "roots add", "outside_scope"},
	}
	for format, wants := range formats {
		t.Run(format, func(t *testing.T) {
			code, out, errOut := brooom(t, "", "worktrees", "--format", format)
			if code != ExitOK {
				t.Fatalf("code %d, stderr %q\n%s", code, errOut, out)
			}
			if format == "plain" && strings.Contains(out, name) {
				t.Errorf("plain listed a non-actionable path:\n%s", out)
			}
			for _, want := range wants {
				if !strings.Contains(out, want) {
					t.Errorf("output lacks %q:\n%s", want, out)
				}
			}
		})
	}

	code, out, errOut := brooom(t, "", append([]string{"worktrees", "--apply", "--yes"}, quarantine...)...)
	if code != ExitOK {
		t.Fatalf("apply: code %d, stderr %q\n%s", code, errOut, out)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Errorf("worktree outside the scope was removed: %v", err)
	}
}

// TestLinkedWorktreeRunNeverRemovesSiblings pins the least-privilege rule: run
// from a linked worktree the scope is that worktree, so a merged sibling
// worktree below the main checkout must not be offered or removed. The main
// worktree is only reachable as the repository that git commands run in.
func TestLinkedWorktreeRunNeverRemovesSiblings(t *testing.T) {
	f := newCleanupFixture(t, nil)
	f.feature("feat/own")
	f.feature("feat/sibling")
	sibling := f.worktreeInRepo("sibling", "feat/sibling")
	own := f.repo.AddWorktree("own", "feat/own")
	f.mergeCommit("feat/own")
	f.mergeCommit("feat/sibling")
	f.publish()
	t.Chdir(own)

	code, out, errOut := brooom(t, "", append([]string{"worktrees", "--apply", "--yes"}, quarantine...)...)
	if code != ExitOK {
		t.Fatalf("code %d, stderr %q\n%s", code, errOut, out)
	}
	if _, err := os.Stat(sibling); err != nil {
		t.Errorf("sibling worktree below the main checkout was removed: %v\n%s", err, out)
	}
	if _, err := os.Stat(own); err != nil {
		t.Errorf("the worktree the command runs in was removed: %v", err)
	}
	if strings.Contains(out, sibling) {
		t.Errorf("sibling worktree was offered:\n%s", out)
	}
}
