package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// writeTemp writes content to name inside a fresh temp dir and returns the path.
func writeTemp(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// jsonStr returns s as a JSON string literal (portable for Windows paths).
func jsonStr(t *testing.T, s string) string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestLoadMissingFileReturnsDefaults(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "nope", "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg, Default()) {
		t.Error("missing file must yield Default()")
	}
}

func TestLoadDirectoryIsError(t *testing.T) {
	dir := t.TempDir()
	_, err := Load(dir)
	if err == nil || !strings.Contains(err.Error(), dir) {
		t.Fatalf("want error naming %s, got %v", dir, err)
	}
}

func TestLoadAccepts(t *testing.T) {
	root := t.TempDir()
	tests := []struct {
		name, content string
		check         func(*testing.T, *Config)
	}{
		{"empty object", `{}`, func(t *testing.T, c *Config) {
			if !reflect.DeepEqual(c, Default()) {
				t.Error("{} must equal defaults")
			}
		}},
		{"BOM", "\xEF\xBB\xBF{}", nil},
		{"trailing whitespace", "{}\n\n  ", nil},
		{"partial override keeps other defaults", `{"thresholds":{"min_age_days":3}}`, func(t *testing.T, c *Config) {
			if c.Thresholds.MinAgeDays != 3 || c.Thresholds.RecentDays != Default().Thresholds.RecentDays {
				t.Errorf("unexpected thresholds %+v", c.Thresholds)
			}
		}},
		{"missing version means current", `{}`, func(t *testing.T, c *Config) {
			if c.Version != CurrentVersion {
				t.Error("version must default to current")
			}
		}},
		{"root kept as written", `{"roots":[{"path":"~/dev"}]}`, func(t *testing.T, c *Config) {
			if c.Roots[0].Path != "~/dev" {
				t.Errorf("root rewritten to %q", c.Roots[0].Path)
			}
		}},
		{"absolute root", `{"roots":[{"path":` + jsonStr(t, root) + `}]}`, nil},
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := Load(writeTemp(t, "config.json", tt.content))
			if err != nil {
				t.Fatal(err)
			}
			if tt.check != nil {
				tt.check(t, cfg)
			}
		})
	}
}

func TestLoadRejects(t *testing.T) {
	tests := []struct {
		name, content, want string
	}{
		{"empty file", ``, "empty"},
		{"whitespace only", " \n\t", "empty"},
		{"unknown key with suggestion", `{"detectors":{"merged-branch":{"mdoe":"ancestor"}}}`,
			`config.json: unknown key "detectors.merged-branch.mdoe" (did you mean "mode"?)`},
		{"unknown top-level key", `{"bogus": 1}`, `unknown key "bogus"`},
		{"wrong type in array element", `{"roots":[{"path":"/a"},{"path":5}]}`, `config.json: roots[1].path: expected string, got number`},
		{"wrong type object", `{"git": []}`, "git: expected object, got array"},
		{"wrong type bool", `{"update_check": "yes"}`, "update_check: expected bool, got string"},
		{"fractional int", `{"thresholds":{"min_age_days":1.5}}`, "thresholds.min_age_days: expected integer, got 1.5"},
		{"top level not an object", `[]`, "expected object, got array"},
		{"syntax error line and column", "{\n  \"a\": }", "line 2, column"},
		{"truncated", `{"a":`, "line 1, column"},
		{"trailing garbage", `{} x`, "unexpected data after the top-level value at line 1, column 4"},
		{"second value", "{}\n{}", "unexpected data after the top-level value at line 2"},
		{"version too new", `{"version": 2}`, "upgrade Brooom"},
		{"version zero", `{"version": 0}`, "version 0 is invalid"},
		{"version negative", `{"version": -1}`, "invalid"},
		{"validation failure", `{"output":{"format":"xml"}}`, "output.format"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(writeTemp(t, "config.json", tt.content))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("want error containing %q, got %v", tt.want, err)
			}
		})
	}
}

func TestLoadValidationErrorIsInvalid(t *testing.T) {
	_, err := Load(writeTemp(t, "config.json", `{"output":{"format":"xml","color":"blue"}}`))
	var ve *ValidationError
	if !errors.Is(err, ErrInvalid) || !errors.As(err, &ve) || len(ve.Problems) != 2 {
		t.Fatalf("want ValidationError with 2 problems, got %v", err)
	}
}

func TestLoadSizeCap(t *testing.T) {
	big := `{"agent":{"model":"` + strings.Repeat("a", maxConfigBytes) + `"}}`
	_, err := Load(writeTemp(t, "config.json", big))
	if err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("want size error, got %v", err)
	}
}

func TestLoadSlicesAndMapsReplaceDefaults(t *testing.T) {
	cfg, err := Load(writeTemp(t, "config.json",
		`{"git":{"protected_branches":["only"]},"trash":{"per_detector":{"worktrees":"quarantine"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg.Git.ProtectedBranches, []string{"only"}) {
		t.Errorf("protected_branches not replaced: %v", cfg.Git.ProtectedBranches)
	}
	if !reflect.DeepEqual(cfg.Git.BaseBranches, Default().Git.BaseBranches) {
		t.Error("untouched list must keep its default")
	}
	if len(cfg.Trash.PerDetector) != 1 {
		t.Errorf("per_detector merged with defaults: %v", cfg.Trash.PerDetector)
	}
}

func TestLoadNullYieldsEmpty(t *testing.T) {
	p := writeTemp(t, "config.json", `{"roots":null,"scan":{"skip_dirs":null}}`)
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Roots == nil || len(cfg.Roots) != 0 || len(cfg.Scan.SkipDirs) != 0 {
		t.Errorf("null must yield empty: %#v", cfg.Roots)
	}
	// null for a list that must not be empty is caught by validation.
	_, err = Load(writeTemp(t, "config.json", `{"git":{"protected_branches":null}}`))
	if err == nil || !strings.Contains(err.Error(), "git.protected_branches") {
		t.Fatalf("want protected_branches problem, got %v", err)
	}
}

func TestEnsureDirs(t *testing.T) {
	home := filepath.Join(t.TempDir(), "h")
	t.Setenv(HomeEnv, home)
	d, err := EnsureDirs()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{d.Home, d.Cache, d.Sessions, d.Quarantine} {
		fi, err := os.Stat(p)
		if err != nil || !fi.IsDir() {
			t.Fatalf("%s not created: %v", p, err)
		}
	}
	// Existing directories are left alone (idempotent, no chmod).
	if _, err := EnsureDirs(); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureDirsExistingFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv(HomeEnv, home)
	blocker := filepath.Join(home, "cache")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := EnsureDirs()
	if err == nil || !strings.Contains(err.Error(), blocker) {
		t.Fatalf("want error naming %s, got %v", blocker, err)
	}
}

func TestEnsureDirsPermissions(t *testing.T) {
	if isWindows() {
		t.Skip("permission bits are not meaningful on Windows")
	}
	home := filepath.Join(t.TempDir(), "h")
	t.Setenv(HomeEnv, home)
	d, err := EnsureDirs()
	if err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(d.Sessions)
	if fi.Mode().Perm() != 0o700 {
		t.Errorf("sessions mode %v, want 0700", fi.Mode().Perm())
	}
}
