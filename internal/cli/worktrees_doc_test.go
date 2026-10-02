package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWorktreesDocumentPlainException pins that the plain format omits the
// informational out-of-scope worktree findings and that users can learn so
// from the help text and the README instead of finding entries silently
// missing.
func TestWorktreesDocumentPlainException(t *testing.T) {
	cmd, _, err := NewRootCommand().Find([]string{"sweep"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cmd.Long, "plain format") || !strings.Contains(cmd.Long, "omits") {
		t.Errorf("sweep help does not mention the plain exception:\n%s", cmd.Long)
	}
	readme, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(readme), "--format plain") {
		t.Error("README does not mention that plain omits the informational worktree findings")
	}
}
