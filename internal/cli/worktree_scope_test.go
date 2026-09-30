package cli

import (
	"os"
	"strings"
	"testing"
)

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
