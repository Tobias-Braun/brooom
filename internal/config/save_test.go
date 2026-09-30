package config

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func roundTripConfigs(t *testing.T) map[string]*Config {
	root := t.TempDir()
	custom := Default()
	custom.Roots = []Root{{
		Path:       root,
		Exclude:    []string{"node_modules", "**/vendor"},
		Thresholds: &ThresholdOverrides{MinAgeDays: intp(30), MinSizeBytes: int64p(1 << 40)},
		Detectors:  map[string]bool{"build-artifacts": false},
	}}
	custom.Git.ProtectedBranches = []string{"main", "prod/*"}
	custom.Detectors.StaleBranch.MinAgeDays = 10
	custom.Detectors.AIArtifacts.MinAgeDays = intp(3)
	custom.Detectors.AIArtifacts.Tools = map[string]bool{"cursor": false}
	custom.Detectors.Logs.Extra = []CatalogTool{{ID: "x-y", Name: "X", Project: []string{"a/b"}}}
	custom.Trash = Trash{Strategy: StrategyQuarantine, PerDetector: map[string]TrashStrategy{"worktrees": StrategyTrash}, QuarantineRetentionDays: 0}
	custom.Output.Format = "json"
	custom.Scan.SkipDirs = []string{"cache"}
	custom.Agent = Agent{Provider: "anthropic", APIKeyEnv: "ANTHROPIC_API_KEY"}
	custom.UpdateCheck = true
	deleting := Default()
	deleting.Trash.Strategy = StrategyDelete
	deleting.Trash.AllowDelete = true
	return map[string]*Config{"default": Default(), "custom": custom, "delete opt-in": deleting}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	for name, cfg := range roundTripConfigs(t) {
		for _, full := range []bool{false, true} {
			t.Run(name, func(t *testing.T) {
				p := filepath.Join(t.TempDir(), "sub", "config.json")
				var err error
				if full {
					err = SaveFull(p, cfg)
				} else {
					err = Save(p, cfg)
				}
				if err != nil {
					t.Fatal(err)
				}
				got, err := Load(p)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(got, cfg) {
					t.Errorf("round trip differs (full=%v):\n got %+v\nwant %+v", full, got, cfg)
				}
			})
		}
	}
}

func TestSaveWritesMinimalDiff(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	if err := Save(p, Default()); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(p)
	if strings.TrimSpace(string(data)) != "{\n  \"version\": 1\n}" || !bytes.HasSuffix(data, []byte("\n")) {
		t.Errorf("default config must be just the version: %q", data)
	}
	cfg := Default()
	cfg.Thresholds.MinAgeDays = 3
	cfg.Git.ProtectedBranches = []string{"main"}
	cfg.Detectors.StaleBranch.MinAgeDays = 7
	if err := Save(p, cfg); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(p)
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	if len(m) != 4 { // version, thresholds, git, detectors
		t.Errorf("unexpected keys in %s", data)
	}
	th := m["thresholds"].(map[string]any)
	if len(th) != 1 || th["min_age_days"] != float64(3) {
		t.Errorf("thresholds must contain only the changed value: %v", th)
	}
	git := m["git"].(map[string]any)
	if len(git) != 1 || len(git["protected_branches"].([]any)) != 1 {
		t.Errorf("git must contain the whole replaced list only: %v", git)
	}
	if _, ok := m["detectors"].(map[string]any)["stale-branch"]; !ok || len(m["detectors"].(map[string]any)) != 1 {
		t.Errorf("only the changed detector block expected: %v", m["detectors"])
	}
}

func TestSaveDoesNotMutateInput(t *testing.T) {
	cfg := Default()
	cfg.Version = 0
	if err := Save(filepath.Join(t.TempDir(), "c.json"), cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Version != 0 {
		t.Error("Save must not modify the caller's config")
	}
}

func TestSaveFullContainsEveryTopLevelKey(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	if err := SaveFull(p, Default()); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(p)
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	for k := range jsonFields(reflect.TypeOf(Config{})) {
		if _, ok := m[k]; !ok {
			t.Errorf("full document lacks %q", k)
		}
	}
	if !bytes.HasSuffix(data, []byte("\n")) {
		t.Error("missing trailing newline")
	}
	full, err := Marshal(Default(), true)
	if err != nil || !bytes.Equal(full, data) {
		t.Errorf("Marshal(full) differs from SaveFull output: %v", err)
	}
}

func TestSaveRefusesInvalidConfig(t *testing.T) {
	for name, save := range map[string]func(string, *Config) error{"Save": Save, "SaveFull": SaveFull} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			p := filepath.Join(dir, "config.json")
			existing := []byte("{\"version\": 1}\n")
			if err := os.WriteFile(p, existing, 0o600); err != nil {
				t.Fatal(err)
			}
			cfg := Default()
			cfg.Output.Format = "xml"
			err := save(p, cfg)
			if err == nil || !strings.Contains(err.Error(), "output.format") {
				t.Fatalf("got %v", err)
			}
			got, _ := os.ReadFile(p)
			if !bytes.Equal(got, existing) {
				t.Error("existing file changed by refused save")
			}
			entries, _ := os.ReadDir(dir)
			if len(entries) != 1 {
				t.Errorf("stray files left: %v", entries)
			}
		})
	}
}

func TestSaveNewFileInvalidNotCreated(t *testing.T) {
	p := filepath.Join(t.TempDir(), "new", "config.json")
	cfg := Default()
	cfg.Trash.Strategy = StrategyDelete
	if err := Save(p, cfg); err == nil {
		t.Fatal("delete without allow_delete must be refused")
	}
	if _, err := os.Stat(filepath.Dir(p)); err == nil {
		t.Error("nothing may be created for an invalid config")
	}
}

func TestWriteFailureLeavesNoTempFile(t *testing.T) {
	// Renaming a file over a directory fails on every OS.
	dir := t.TempDir()
	target := filepath.Join(dir, "config.json")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := Save(target, Default()); err == nil {
		t.Fatal("want error when target is a directory")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 || entries[0].Name() != "config.json" {
		t.Errorf("temp file left behind: %v", entries)
	}
}

func TestFailedWriteKeepsExistingFile(t *testing.T) {
	if isWindows() {
		t.Skip("read-only directories are not enforced the same way on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	for name, save := range map[string]func(string, *Config) error{"Save": Save, "SaveFull": SaveFull} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			p := filepath.Join(dir, "config.json")
			existing := []byte("{\"version\": 1}\n")
			if err := os.WriteFile(p, existing, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(dir, 0o500); err != nil {
				t.Fatal(err)
			}
			defer os.Chmod(dir, 0o700) //nolint:errcheck // best-effort restore so TempDir cleanup works
			cfg := Default()
			cfg.Thresholds.MinAgeDays = 1
			if err := save(p, cfg); err == nil {
				t.Fatal("want write error")
			}
			got, _ := os.ReadFile(p)
			if !bytes.Equal(got, existing) {
				t.Error("existing config changed by failed save")
			}
		})
	}
}

func TestSavePermissions(t *testing.T) {
	if isWindows() {
		t.Skip("permission bits are not meaningful on Windows")
	}
	dir := filepath.Join(t.TempDir(), "a", "b")
	p := filepath.Join(dir, "config.json")
	if err := Save(p, Default()); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(p)
	di, _ := os.Stat(dir)
	if fi.Mode().Perm() != 0o600 || di.Mode().Perm() != 0o700 {
		t.Errorf("file %v dir %v, want 0600 / 0700", fi.Mode().Perm(), di.Mode().Perm())
	}
}
