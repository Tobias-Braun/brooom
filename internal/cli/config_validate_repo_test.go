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
	cfg, _, _ := configEnv(t)
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

// TestConfigNotesRemovedRoots: a config of an earlier release with a root
// registry still loads, and every command that reads it says once that the
// key is ignored and what replaced it.
func TestConfigNotesRemovedRoots(t *testing.T) {
	cfg, work, _ := configEnv(t)
	t.Chdir(testutil.ResolvedTempDir(t))
	quoted, err := json.Marshal(filepath.Join(work, "ws"))
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, cfg, `{"version":1,"roots":[{"path":`+string(quoted)+`}]}`)
	for _, args := range [][]string{{"config", "validate"}, {"scan", work}} {
		code, _, errOut := run(t, args...)
		if code != ExitOK || strings.Count(errOut, "note: config: roots:") != 1 || !strings.Contains(errOut, "brooom sweep ~/code") {
			t.Errorf("%v: code=%d err=%q", args, code, errOut)
		}
	}
	// The empty list `config init` used to write says nothing.
	writeFile(t, cfg, `{"version":1,"roots":[]}`)
	if code, _, errOut := run(t, "config", "validate"); code != ExitOK || strings.Contains(errOut, "roots") {
		t.Errorf("empty roots: code=%d err=%q", code, errOut)
	}
}
