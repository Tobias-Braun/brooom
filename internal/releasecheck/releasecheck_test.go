// Package releasecheck holds tests that pin the release hardening in files Go
// does not build: the CI workflows, the GoReleaser config and the Windows
// install script. Those can only be executed on their own platforms in CI, so
// these tests make sure a change cannot silently drop the guards again.
//
// They are deliberately coarse tripwires on substrings, not proof of behavior:
// the behavior itself is exercised by the release-check workflow (install.ps1
// under both PowerShell editions, the macOS smoke test). Renaming a guarded
// token means updating the matching case here.
package releasecheck

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// repoFile reads a file relative to the repository root (two levels up).
func repoFile(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestReleaseHardeningGuards(t *testing.T) {
	cases := []struct {
		name    string
		file    string
		want    []string
		notWant []string
	}{
		{
			name: "ci runs the trash tests without cgo on arm64 and Intel macOS",
			file: ".github/workflows/ci.yml",
			want: []string{"macos-latest", "macos-15-intel", `CGO_ENABLED: "0"`, "go test -count=1 -v ./internal/trash/..."},
		},
		{
			name: "release check smoke-tests darwin snapshots and install.ps1 in both shells",
			file: ".github/workflows/release-check.yml",
			want: []string{
				"smoke-darwin.sh", "macos-15-intel", "internal/trash/**",
				"shell: [powershell, pwsh]", "test-install.ps1 -DistDir dist",
			},
		},
		{
			name: "release refuses tags outside main",
			file: ".github/workflows/release.yml",
			want: []string{"git merge-base --is-ancestor", "origin/main"},
		},
		{
			name: "cask clears the quarantine attribute until notarized",
			file: ".goreleaser.yaml",
			want: []string{"hooks:", "com.apple.quarantine", "#{staged_path}/brooom"},
		},
		{
			name:    "install.ps1 reads the releases API and the 32-bit architecture variable",
			file:    "scripts/install.ps1",
			want:    []string{"api.github.com/repos/Tobias-Braun/brooom/releases/latest", "tag_name", "PROCESSOR_ARCHITEW6432"},
			notWant: []string{"-MaximumRedirection"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := repoFile(t, tc.file)
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("%s does not contain %q", tc.file, w)
				}
			}
			for _, w := range tc.notWant {
				if strings.Contains(got, w) {
					t.Errorf("%s must not contain %q", tc.file, w)
				}
			}
		})
	}
}

// TestReleaseTagCheckRunsBeforeGoReleaser makes sure the reachability check
// comes before the step that publishes; a check after it would be useless.
func TestReleaseTagCheckRunsBeforeGoReleaser(t *testing.T) {
	got := repoFile(t, ".github/workflows/release.yml")
	check := strings.Index(got, "merge-base --is-ancestor")
	publish := strings.Index(got, "goreleaser/goreleaser-action")
	if check < 0 || publish < 0 || check > publish {
		t.Fatalf("tag check at %d must precede goreleaser at %d", check, publish)
	}
}
