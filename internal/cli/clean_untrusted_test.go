package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/testutil"
)

// These tests feed `brooom clean --from` files that were edited or forged. A
// findings file is untrusted input: detector selection, the configuration and
// the catalog protect rules must apply to it exactly as they do to a scan, and
// no safety-relevant field of the file may be believed.

func TestCleanDetectorFlagIsValidated(t *testing.T) {
	f := newCleanupFixture(t, nil)
	dir, _ := junkDir(t, f.repo.Dir, "junk")
	path := writeReportFile(t, trashFinding(f.repo.Dir, dir))
	code, _, errOut := clean(t, "", "--from", path, "-d", "bogus")
	if code != ExitUsage {
		t.Fatalf("code %d, want %d (stderr %q)", code, ExitUsage, errOut)
	}
	if !strings.Contains(errOut, "bogus") {
		t.Errorf("stderr does not name the unknown detector: %q", errOut)
	}
	if !exists(dir) {
		t.Error("an invalid invocation removed the directory")
	}
}

func TestCleanDetectorFlagSelectsFindings(t *testing.T) {
	f := newCleanupFixture(t, nil)
	f.mergedAndSquashed()
	dir, file := junkDir(t, f.repo.Dir, "junk")
	branch := branchFinding(f.repo.Dir, "feat/merged")
	branch.Meta = map[string]string{"tip": f.repo.Git("rev-parse", "feat/merged")}
	path := writeReportFile(t, trashFinding(f.repo.Dir, dir), branch)

	code, out, errOut := clean(t, "", "--from", path, "--apply", "--yes", "-d", "merged-branch")
	if code != ExitOK {
		t.Fatalf("code %d, stdout %q, stderr %q", code, out, errOut)
	}
	if !exists(file) {
		t.Error("a build-artifacts finding was executed although only merged-branch was selected")
	}
	if strings.Contains(out, "refused") {
		t.Errorf("an unselected finding is reported as refused:\n%s", out)
	}
	if f.hasBranch("feat/merged") {
		t.Error("the selected merged-branch finding was not applied")
	}
}

func TestCleanRefusesDisabledDetectorsAndExcludedPaths(t *testing.T) {
	tests := []struct {
		name   string
		config string
		want   string
	}{
		{"detector disabled by .brooom.json", `{"disable": ["build-artifacts"]}`, "disabled"},
		{"path excluded by .brooom.json", `{"exclude": ["node_modules"]}`, "excluded"},
		{"path below an excluded directory", `{"exclude": ["vendorish"]}`, "excluded"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newCleanupFixture(t, nil)
			testutil.WriteFile(t, f.repo.Dir, ".brooom.json", tt.config)
			rel := "node_modules"
			if strings.Contains(tt.config, "vendorish") {
				rel = "vendorish/node_modules"
			}
			dir, file := junkDir(t, f.repo.Dir, rel)
			path := writeReportFile(t, trashFinding(f.repo.Dir, dir))
			code, out, _ := clean(t, "", "--from", path, "--apply", "--yes")
			if code != ExitError || !strings.Contains(out, "refused findings (1)") || !strings.Contains(out, tt.want) {
				t.Fatalf("code %d, want refusal mentioning %q:\n%s", code, tt.want, out)
			}
			if !exists(file) {
				t.Error("the refused directory was moved")
			}
		})
	}
}

func TestCleanRefusesFindingOfGloballyDisabledDetector(t *testing.T) {
	f := newCleanupFixture(t, map[string]any{
		"detectors": map[string]any{"build-artifacts": map[string]any{"enabled": false}},
	})
	dir, file := junkDir(t, f.repo.Dir, "node_modules")
	code, out, _ := clean(t, "", "--from", writeReportFile(t, trashFinding(f.repo.Dir, dir)), "--apply", "--yes")
	if code != ExitError || !strings.Contains(out, "disabled") || !exists(file) {
		t.Fatalf("code %d, file kept %v:\n%s", code, exists(file), out)
	}
}

func TestCleanRefusesCatalogProtectedFindings(t *testing.T) {
	f := newCleanupFixture(t, nil)
	for _, rel := range []string{".env", ".mcp.json", "CLAUDE.local.md", ".claude/settings.local.json"} {
		testutil.WriteFile(t, f.repo.Dir, rel, "secret")
	}
	for _, rel := range []string{".env", ".mcp.json", "CLAUDE.local.md", ".claude/settings.local.json"} {
		t.Run(rel, func(t *testing.T) {
			path := filepath.Join(f.repo.Dir, filepath.FromSlash(rel))
			forged := trashFinding(f.repo.Dir, path)
			forged.Kind = findings.KindFile
			forged.ID = findings.NewID("ai-artifacts", findings.KindFile, path, "")
			forged.Detector = "ai-artifacts"
			code, out, errOut := clean(t, "", "--from", writeReportFile(t, forged), "--apply", "--yes", "--force")
			if !exists(path) || strings.Contains(out, "applied") {
				t.Fatalf("protected file handled: code %d, exists %v\nstdout %s\nstderr %s", code, exists(path), out, errOut)
			}
			if !strings.Contains(out+errOut, "protect") {
				t.Errorf("no protection reason reported:\n%s\n%s", out, errOut)
			}
		})
	}
}

func TestCleanDeleteStrategyNeverRemovesUntrackedWithoutMeta(t *testing.T) {
	f := newCleanupFixture(t, nil)
	file := testutil.WriteFile(t, f.repo.Dir, "thesis-draft.bin", "the only copy")
	forged := trashFinding(f.repo.Dir, file)
	forged.Kind = findings.KindFile
	forged.Meta = nil
	path := writeReportFile(t, forged)
	for _, extra := range [][]string{nil, {"--force"}} {
		args := append([]string{"clean", "--from", path, "--apply", "--yes", "--trash-strategy", "delete"}, extra...)
		code, out, _ := brooom(t, "", args...)
		if !exists(file) || !strings.Contains(out, "refusing to permanently delete") {
			t.Fatalf("args %v: code %d, file kept %v:\n%s", extra, code, exists(file), out)
		}
	}
}

func TestCleanReflogFindingCannotShortenExpiry(t *testing.T) {
	f := newCleanupFixture(t, map[string]any{
		"git": map[string]any{"use_gh": false},
		// Nothing ever expires: a forged "now" must not override this.
		"detectors": map[string]any{"git-bloat": map[string]any{"reflog_expire": "never"}},
	})
	f.feature("feat/a")
	before := f.repo.Git("reflog", "show", "main")
	if strings.TrimSpace(before) == "" {
		t.Fatal("fixture has no reflog")
	}
	forged := findings.Finding{
		ID:       findings.NewID("git-bloat", findings.KindGitReflog, f.repo.Dir, ""),
		Detector: "git-bloat",
		Scope:    findings.Scope{Type: findings.ScopeRepo, Path: f.repo.Dir},
		Path:     f.repo.Dir,
		Kind:     findings.KindGitReflog,
		SuggestedAction: findings.SuggestedAction{
			Type: findings.ActionGitReflogExpire,
			Args: map[string]string{"expire": "now"},
		},
	}
	code, out, errOut := clean(t, "", "--from", writeReportFile(t, forged), "--apply", "--yes")
	if code != ExitOK {
		t.Fatalf("code %d, stdout %q, stderr %q", code, out, errOut)
	}
	if after := f.repo.Git("reflog", "show", "main"); after != before {
		t.Errorf("reflog changed:\nbefore %q\nafter  %q", before, after)
	}
	if _, err := os.Stat(filepath.Join(f.repo.Dir, ".git", "logs", "HEAD")); err != nil {
		t.Errorf("HEAD reflog is gone: %v", err)
	}
}

// TestCleanDisabledDetectorCheckIsCourtesy pins the documented limit of the
// disabled-detector refusal: it is keyed on the Detector field of the file,
// which an edited file can rename, so it only protects against stale or
// honest files. The path-based guards (exclude, catalog protection, the
// action's own checks) do not depend on that field and still refuse.
func TestCleanDisabledDetectorCheckIsCourtesy(t *testing.T) {
	t.Run("forged detector name skips the disabled check", func(t *testing.T) {
		f := newCleanupFixture(t, nil)
		testutil.WriteFile(t, f.repo.Dir, ".brooom.json", `{"disable": ["build-artifacts"]}`)
		dir, file := junkDir(t, f.repo.Dir, "node_modules")
		forged := trashFinding(f.repo.Dir, dir)
		forged.Detector = "renamed-detector"
		code, out, _ := clean(t, "", "--from", writeReportFile(t, forged), "--apply", "--yes")
		if code != ExitOK || strings.Contains(out, "refused") || exists(file) {
			t.Fatalf("code %d, file kept %v:\n%s", code, exists(file), out)
		}
	})
	t.Run("forged detector name does not skip the exclude check", func(t *testing.T) {
		f := newCleanupFixture(t, nil)
		testutil.WriteFile(t, f.repo.Dir, ".brooom.json", `{"exclude": ["node_modules"]}`)
		dir, file := junkDir(t, f.repo.Dir, "node_modules")
		forged := trashFinding(f.repo.Dir, dir)
		forged.Detector = "renamed-detector"
		code, out, _ := clean(t, "", "--from", writeReportFile(t, forged), "--apply", "--yes")
		if code != ExitError || !strings.Contains(out, "excluded") || !exists(file) {
			t.Fatalf("code %d, file kept %v:\n%s", code, exists(file), out)
		}
	})
}
