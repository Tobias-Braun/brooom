package cli

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/testutil"
)

// TestConfigValidateChecksRepoConfig pins that validate reports what a scan
// would: a repository .brooom.json that scan rejects must not validate ok.
func TestConfigValidateChecksRepoConfig(t *testing.T) {
	cfg, _, _ := rootsEnv(t)
	writeFile(t, cfg, `{"version":1}`)
	repo := testutil.NewRepo(t)
	t.Chdir(repo.Dir)
	file := filepath.Join(repo.Dir, config.RepoConfigFileName)

	t.Run("no repository file", func(t *testing.T) {
		code, out, _ := run(t, "config", "validate")
		if code != ExitOK || strings.TrimSpace(out) != "ok" {
			t.Fatalf("code=%d out=%q", code, out)
		}
	})
	t.Run("valid repository file", func(t *testing.T) {
		writeFile(t, file, `{"version":1}`)
		code, out, errOut := run(t, "config", "validate")
		if code != ExitOK || !strings.Contains(out, "repository config") || !strings.Contains(out, "depends on the current directory") {
			t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
		}
	})
	t.Run("unknown key", func(t *testing.T) {
		writeFile(t, file, `{"bogus":1}`)
		code, out, errOut := run(t, "config", "validate")
		if code != ExitError || out != "" {
			t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
		}
		for _, want := range []string{file, `unknown key "bogus"`, "depends on the current directory"} {
			if !strings.Contains(errOut, want) {
				t.Errorf("stderr lacks %q:\n%s", want, errOut)
			}
		}
	})
	t.Run("outside a repository the file is not consulted", func(t *testing.T) {
		t.Chdir(testutil.ResolvedTempDir(t))
		code, out, _ := run(t, "config", "validate")
		if code != ExitOK || strings.TrimSpace(out) != "ok" {
			t.Fatalf("code=%d out=%q", code, out)
		}
	})
}

// TestConfigValidateWarnsAboutMissingRoots checks that a root that does not
// exist is only a warning: unmounted volumes are legitimate.
func TestConfigValidateWarnsAboutMissingRoots(t *testing.T) {
	cfg, work, _ := rootsEnv(t)
	t.Chdir(testutil.ResolvedTempDir(t))
	gone := filepath.Join(work, "gone")
	quoted, err := json.Marshal(gone)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, cfg, `{"version":1,"roots":[{"path":`+string(quoted)+`}]}`)
	code, out, errOut := run(t, "config", "validate")
	if code != ExitOK || strings.TrimSpace(out) != "ok" {
		t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
	}
	if !strings.Contains(errOut, "warning: roots[0].path") || !strings.Contains(errOut, gone) {
		t.Errorf("no warning for the missing root:\n%s", errOut)
	}
}
