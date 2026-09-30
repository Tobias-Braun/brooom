package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/findings"
)

func TestCheckAliasRefusesProtectedIdentity(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "brooom-home")
	t.Setenv(config.HomeEnv, home)
	t.Setenv("HOME", filepath.Join(root, "user"))
	t.Setenv("USERPROFILE", filepath.Join(root, "user"))
	sessions := filepath.Join(home, "sessions")
	repo := filepath.Join(root, "repo")
	for _, d := range []string{sessions, filepath.Join(repo, ".git"), filepath.Join(repo, "build")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	trash := findings.Finding{SuggestedAction: findings.SuggestedAction{Type: findings.ActionTrash}}
	tests := []struct {
		name string
		f    findings.Finding
		path string
		want string
	}{
		{"sessions directory", trash, sessions, "session or quarantine"},
		{"ordinary build dir", trash, filepath.Join(repo, "build"), ""},
		{"non-trash action is not checked", findings.Finding{}, sessions, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := checkAlias(tc.f, tc.path)
			if tc.want == "" && got != "" || !strings.Contains(got, tc.want) {
				t.Fatalf("checkAlias = %q, want %q", got, tc.want)
			}
		})
	}
}
