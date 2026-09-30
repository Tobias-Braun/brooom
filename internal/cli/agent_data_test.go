package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// claudeDir is Claude Code's directory name for a repository path: every
// character that is not an ASCII letter or digit becomes "-".
func claudeDir(repo string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			return r
		}
		return '-'
	}, repo)
}

// oldTranscript writes a transcript below ~/.claude/projects/<dir> that is
// 100 days old and returns its path.
func oldTranscript(t *testing.T, dir, name string) string {
	t.Helper()
	p := filepath.Join(os.Getenv("HOME"), ".claude", "projects", dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().AddDate(0, 0, -100)
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestAfterAgentsSweepsTheRepositorysTranscripts covers #288 end to end: the
// transcripts Claude Code keeps for this repository and for its worktrees are
// swept without any flag, those of another repository are not even listed.
func TestAfterAgentsSweepsTheRepositorysTranscripts(t *testing.T) {
	f := newCleanupFixture(t, nil)
	repo, err := filepath.EvalSymlinks(f.repo.Dir)
	if err != nil {
		t.Fatal(err)
	}
	own := oldTranscript(t, claudeDir(repo), "session.jsonl")
	worktree := oldTranscript(t, claudeDir(filepath.Join(repo, ".claude", "worktrees", "agent-1")), "session.jsonl")
	sibling := oldTranscript(t, claudeDir(repo)+"-site", "session.jsonl")
	other := oldTranscript(t, claudeDir(filepath.Join(filepath.Dir(repo), "other")), "session.jsonl")

	code, out, errOut := brooom(t, "", "sweep", "after-agents", "-d", "ai-artifacts", "--dry-run")
	if code != ExitOK {
		t.Fatalf("code %d, stderr %q", code, errOut)
	}
	for _, p := range []string{sibling, other} {
		if strings.Contains(out, filepath.Dir(p)) {
			t.Errorf("another repository's transcripts were listed:\n%s", out)
		}
	}

	code, out, errOut = brooom(t, "", append([]string{"sweep", "after-agents", "-d", "ai-artifacts", "--yes"}, quarantine...)...)
	if code != ExitOK {
		t.Fatalf("code %d, stderr %q\n%s", code, errOut, out)
	}
	for _, p := range []string{own, worktree} {
		if exists(p) {
			t.Errorf("%s was not swept:\n%s", p, out)
		}
	}
	for _, p := range []string{sibling, other} {
		if !exists(p) {
			t.Errorf("%s of another repository was removed", p)
		}
	}
}
