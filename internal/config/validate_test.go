package config

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func isWindows() bool { return runtime.GOOS == "windows" }
func isDarwin() bool  { return runtime.GOOS == "darwin" }

func intp(v int) *int       { return &v }
func int64p(v int64) *int64 { return &v }

func TestDefaultIsValid(t *testing.T) {
	if err := Default().Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestValidateRejects(t *testing.T) {
	abs := t.TempDir()
	tests := []struct {
		name   string
		mutate func(*Config)
		field  string // must appear in the error
	}{
		{"version too new", func(c *Config) { c.Version = 99 }, "version"},
		{"version zero", func(c *Config) { c.Version = 0 }, "version"},
		{"root empty", func(c *Config) { c.Roots = []Root{{Path: ""}} }, "roots[0].path"},
		{"root relative", func(c *Config) { c.Roots = []Root{{Path: "rel/dir"}} }, "roots[0].path"},
		{"root undefined var", func(c *Config) { c.Roots = []Root{{Path: "$BROOOM_TEST_UNSET_VAR/x"}} }, "roots[0].path"},
		{"root duplicate after cleaning", func(c *Config) {
			c.Roots = []Root{{Path: abs}, {Path: filepath.Join(abs, "sub", "..")}}
		}, "roots[1].path"},
		{"root bad glob", func(c *Config) { c.Roots = []Root{{Path: abs, Exclude: []string{"[a"}}} }, "roots[0].exclude[0]"},
		{"root empty glob", func(c *Config) { c.Roots = []Root{{Path: abs, Exclude: []string{""}}} }, "roots[0].exclude[0]"},
		{"root unknown detector", func(c *Config) {
			c.Roots = []Root{{Path: abs, Detectors: map[string]bool{"nope": false}}}
		}, "roots[0].detectors.nope"},
		{"root negative threshold", func(c *Config) {
			c.Roots = []Root{{Path: abs, Thresholds: &ThresholdOverrides{MinAgeDays: intp(-1)}}}
		}, "roots[0].thresholds.min_age_days"},
		{"root negative size", func(c *Config) {
			c.Roots = []Root{{Path: abs, Thresholds: &ThresholdOverrides{MinSizeBytes: int64p(-1)}}}
		}, "roots[0].thresholds.min_size_bytes"},
		{"root negative recent", func(c *Config) {
			c.Roots = []Root{{Path: abs, Thresholds: &ThresholdOverrides{RecentDays: intp(-1)}}}
		}, "roots[0].thresholds.recent_days"},
		{"negative min age", func(c *Config) { c.Thresholds.MinAgeDays = -1 }, "thresholds.min_age_days"},
		{"negative min size", func(c *Config) { c.Thresholds.MinSizeBytes = -1 }, "thresholds.min_size_bytes"},
		{"negative recent", func(c *Config) { c.Thresholds.RecentDays = -1 }, "thresholds.recent_days"},
		{"stale age", func(c *Config) { c.Detectors.StaleBranch.MinAgeDays = -1 }, "detectors.stale-branch.min_age_days"},
		{"worktrees age", func(c *Config) { c.Detectors.Worktrees.MinAgeDays = -1 }, "detectors.worktrees.min_age_days"},
		{"loose objects", func(c *Config) { c.Detectors.GitBloat.LooseObjectsThreshold = -1 }, "loose_objects_threshold"},
		{"pack count", func(c *Config) { c.Detectors.GitBloat.PackCountThreshold = -1 }, "pack_count_threshold"},
		{"reflog bytes", func(c *Config) { c.Detectors.GitBloat.ReflogThresholdBytes = -1 }, "reflog_threshold_bytes"},
		{"blob bytes", func(c *Config) { c.Detectors.GitBloat.LargeBlobBytes = -1 }, "large_blob_bytes"},
		{"large untracked", func(c *Config) { c.Detectors.LargeUntracked.MinSizeBytes = -1 }, "detectors.large-untracked.min_size_bytes"},
		{"ai age", func(c *Config) { c.Detectors.AIArtifacts.MinAgeDays = intp(-1) }, "detectors.ai-artifacts.min_age_days"},
		{"logs age", func(c *Config) { c.Detectors.Logs.MinAgeDays = intp(-1) }, "detectors.log-and-runtime-files.min_age_days"},
		{"inactive days", func(c *Config) { c.Detectors.BuildArtifacts.InactiveDays = -1 }, "inactive_days"},
		{"output format", func(c *Config) { c.Output.Format = "xml" }, "output.format"},
		{"output color", func(c *Config) { c.Output.Color = "blue" }, "output.color"},
		{"trash strategy", func(c *Config) { c.Trash.Strategy = "shred" }, "trash.strategy"},
		{"trash empty strategy", func(c *Config) { c.Trash.Strategy = "" }, "trash.strategy"},
		{"per detector strategy", func(c *Config) { c.Trash.PerDetector = map[string]TrashStrategy{"worktrees": "shred"} }, "trash.per_detector.worktrees"},
		{"per detector unknown", func(c *Config) { c.Trash.PerDetector = map[string]TrashStrategy{"nope": StrategyTrash} }, "trash.per_detector.nope"},
		{"delete default needs opt-in", func(c *Config) { c.Trash.Strategy = StrategyDelete }, "allow_delete"},
		{"delete per detector needs opt-in", func(c *Config) {
			c.Trash.PerDetector = map[string]TrashStrategy{"worktrees": StrategyDelete}
		}, "trash.per_detector.worktrees"},
		{"retention negative", func(c *Config) { c.Trash.QuarantineRetentionDays = -1 }, "trash.quarantine_retention_days"},
		{"merge mode", func(c *Config) { c.Detectors.MergedBranch.Mode = "rebase" }, "detectors.merged-branch.mode"},
		{"protected empty", func(c *Config) { c.Git.ProtectedBranches = nil }, "git.protected_branches"},
		{"protected bad glob", func(c *Config) { c.Git.ProtectedBranches = []string{"[x"} }, "git.protected_branches[0]"},
		{"protected control char", func(c *Config) { c.Git.ProtectedBranches = []string{"a\nb"} }, "git.protected_branches[0]"},
		{"base empty", func(c *Config) { c.Git.BaseBranches = []string{} }, "git.base_branches"},
		{"base empty string", func(c *Config) { c.Git.BaseBranches = []string{""} }, "git.base_branches[0]"},
		{"reflog expire empty", func(c *Config) { c.Detectors.GitBloat.ReflogExpire = "" }, "reflog_expire"},
		{"reflog expire option injection", func(c *Config) { c.Detectors.GitBloat.ReflogExpire = "--all" }, "reflog_expire"},
		{"prune expire whitespace", func(c *Config) { c.Detectors.GitBloat.PruneExpire = "2 weeks ago" }, "prune_expire"},
		{"prune expire control", func(c *Config) { c.Detectors.GitBloat.PruneExpire = "now\x00" }, "prune_expire"},
		{"concurrency", func(c *Config) { c.Scan.Concurrency = -1 }, "scan.concurrency"},
		{"max depth", func(c *Config) { c.Scan.MaxDepth = -1 }, "scan.max_depth"},
		{"skip dir slash", func(c *Config) { c.Scan.SkipDirs = []string{"a/b"} }, "scan.skip_dirs[0]"},
		{"skip dir backslash", func(c *Config) { c.Scan.SkipDirs = []string{`a\b`} }, "scan.skip_dirs[0]"},
		{"skip dir dotdot", func(c *Config) { c.Scan.SkipDirs = []string{".."} }, "scan.skip_dirs[0]"},
		{"skip dir dot", func(c *Config) { c.Scan.SkipDirs = []string{"."} }, "scan.skip_dirs[0]"},
		{"agent provider", func(c *Config) { c.Agent.Provider = "gemini" }, "agent.provider"},
		{"agent key env", func(c *Config) { c.Agent.APIKeyEnv = "1BAD-NAME" }, "agent.api_key_env"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Default()
			tt.mutate(cfg)
			err := cfg.Validate()
			if err == nil || !strings.Contains(err.Error(), tt.field) {
				t.Fatalf("want error naming %q, got %v", tt.field, err)
			}
			if !errors.Is(err, ErrInvalid) {
				t.Error("errors.Is(err, ErrInvalid) must hold")
			}
		})
	}
}

func TestValidateCatalogExtra(t *testing.T) {
	good := CatalogTool{ID: "my-tool", Name: "My tool", Project: []string{".mytool/cache"}}
	tests := []struct {
		name  string
		tools []CatalogTool
		field string
	}{
		{"bad id upper", []CatalogTool{{ID: "MyTool", Name: "x", Project: []string{"a"}}}, "extra[0].id"},
		{"bad id underscore", []CatalogTool{{ID: "my_tool", Name: "x", Project: []string{"a"}}}, "extra[0].id"},
		{"empty id", []CatalogTool{{Name: "x", Project: []string{"a"}}}, "extra[0].id"},
		{"duplicate id", []CatalogTool{good, good}, "extra[1].id"},
		{"empty name", []CatalogTool{{ID: "x", Project: []string{"a"}}}, "extra[0].name"},
		{"no location", []CatalogTool{{ID: "x", Name: "x"}}, "extra[0]"},
		{"absolute project path", []CatalogTool{{ID: "x", Name: "x", Project: []string{"/etc"}}}, "extra[0].project[0]"},
		{"backslash project path", []CatalogTool{{ID: "x", Name: "x", Project: []string{`\etc`}}}, "extra[0].project[0]"},
		{"dotdot project path", []CatalogTool{{ID: "x", Name: "x", Project: []string{"a/../../b"}}}, "extra[0].project[0]"},
		{"dotdot backslash", []CatalogTool{{ID: "x", Name: "x", Project: []string{`a\..\b`}}}, "extra[0].project[0]"},
		{"empty project path", []CatalogTool{{ID: "x", Name: "x", Project: []string{""}}}, "extra[0].project[0]"},
	}
	for _, section := range []string{"ai-artifacts", "log-and-runtime-files"} {
		for _, tt := range tests {
			t.Run(section+"/"+tt.name, func(t *testing.T) {
				cfg := Default()
				if section == "ai-artifacts" {
					cfg.Detectors.AIArtifacts.Extra = tt.tools
				} else {
					cfg.Detectors.Logs.Extra = tt.tools
				}
				err := cfg.Validate()
				want := "detectors." + section + "." + tt.field
				if err == nil || !strings.Contains(err.Error(), want) {
					t.Fatalf("want error naming %q, got %v", want, err)
				}
			})
		}
	}
	cfg := Default()
	cfg.Detectors.AIArtifacts.Extra = []CatalogTool{good, {ID: "user-only", Name: "u", User: []string{"~/.x"}}}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid catalog rejected: %v", err)
	}
}

func TestValidateAccepts(t *testing.T) {
	abs := t.TempDir()
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{"retention zero means never purge", func(c *Config) { c.Trash.QuarantineRetentionDays = 0 }},
		{"delete with opt-in", func(c *Config) { c.Trash.Strategy = StrategyDelete; c.Trash.AllowDelete = true }},
		{"per detector delete with opt-in", func(c *Config) {
			c.Trash.AllowDelete = true
			c.Trash.PerDetector = map[string]TrashStrategy{"worktrees": StrategyDelete}
		}},
		{"double star glob", func(c *Config) { c.Roots = []Root{{Path: abs, Exclude: []string{"**/vendor", "a/**/b"}}} }},
		{"root with overrides", func(c *Config) {
			c.Roots = []Root{{Path: abs, Detectors: map[string]bool{"build-artifacts": false},
				Thresholds: &ThresholdOverrides{MinAgeDays: intp(0)}}}
		}},
		{"tilde root", func(c *Config) { c.Roots = []Root{{Path: "~/dev"}} }},
		{"agent openai", func(c *Config) { c.Agent = Agent{Provider: "openai-compatible", APIKeyEnv: "MY_KEY_1"} }},
		{"expiry with dots", func(c *Config) { c.Detectors.GitBloat.PruneExpire = "now" }},
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Default()
			tt.mutate(cfg)
			if err := cfg.Validate(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestValidateListsEveryProblemInOrder(t *testing.T) {
	cfg := Default()
	cfg.Thresholds.MinAgeDays = -1
	cfg.Output.Format = "xml"
	cfg.Scan.MaxDepth = -1
	err := cfg.Validate()
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("want ValidationError, got %v", err)
	}
	var fields []string
	for _, p := range ve.Problems {
		fields = append(fields, p.Field)
	}
	want := []string{"thresholds.min_age_days", "output.format", "scan.max_depth"}
	if !slices.Equal(fields, want) {
		t.Errorf("problems %v, want %v", fields, want)
	}
	if !strings.Contains(err.Error(), "3 problems") {
		t.Errorf("summary missing: %v", err)
	}
}

func TestValidateRootFilesystemRoot(t *testing.T) {
	root := "/"
	if isWindows() {
		root = `C:\`
	}
	cfg := Default()
	cfg.Roots = []Root{{Path: root}}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "roots[0].path") || !strings.Contains(err.Error(), "filesystem root") {
		t.Fatalf("want filesystem root problem, got %v", err)
	}
}

// TestOutputFormatsMatchSpec pins the locally defined format list to the
// formats documented in docs/SPEC.md (internal/output cannot be imported here).
func TestOutputFormatsMatchSpec(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "SPEC.md"))
	if err != nil {
		t.Fatal(err)
	}
	section := string(data)
	_, section, _ = strings.Cut(section, "## Output formats")
	section, _, _ = strings.Cut(section, "\n## ")
	var got []string
	for _, m := range regexp.MustCompile("(?m)^- `(\\w+)`").FindAllStringSubmatch(section, -1) {
		got = append(got, m[1])
	}
	if !slices.Equal(got, outputFormats) {
		t.Errorf("SPEC formats %v differ from validator list %v", got, outputFormats)
	}
}
