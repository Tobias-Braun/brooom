package aiartifacts

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/detect"
	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/scope"
	"github.com/Tobias-Braun/brooom/internal/testutil"
)

func TestRegistration(t *testing.T) {
	d, ok := detect.Get("ai-artifacts")
	if !ok {
		t.Fatal("ai-artifacts is not registered")
	}
	if d.Category() != detect.CategoryAI {
		t.Errorf("category = %q, want ai", d.Category())
	}
	if _, ok := d.(detect.TargetSource); !ok {
		t.Error("detector does not implement detect.TargetSource")
	}
}

func TestProjectTree(t *testing.T) {
	sandbox(t)
	repo := testutil.NewRepo(t)
	put(t, repo.Dir, ".aider.chat.history.md", 100)
	put(t, repo.Dir, ".aider.input.history", 100)
	put(t, repo.Dir, ".aider.tags.cache.v3/tags.sqlite", 100)
	put(t, repo.Dir, "deep/a/b/.aider.chat.history.md", 100)
	// Tool configuration is never clutter, however old.
	put(t, repo.Dir, ".aider.conf.yml", 100)
	put(t, repo.Dir, "CLAUDE.md", 100)
	put(t, repo.Dir, ".claude/settings.json", 100)
	put(t, repo.Dir, ".claude/commands/fix.md", 100)
	// Pruned: huge directory, nested repository, too young.
	put(t, repo.Dir, "node_modules/pkg/.aider.chat.history.md", 100)
	put(t, repo.Dir, "nested/.git/HEAD", 100)
	put(t, repo.Dir, "nested/.aider.chat.history.md", 100)
	put(t, repo.Dir, "young/.aider.chat.history.md", 3)
	oldDirs(t, repo.Dir)

	env := newEnv(t, config.Default(), repo.Dir)
	before := snapshot(t, repo.Dir)
	got := mustScan(t, env, repoTarget(repo.Dir))
	if !reflect.DeepEqual(snapshot(t, repo.Dir), before) {
		t.Error("the scan modified the repository")
	}

	want := []string{".aider.chat.history.md", ".aider.input.history", ".aider.tags.cache.v3", "deep/a/b/.aider.chat.history.md"}
	if paths := relPaths(t, got, repo.Dir); !reflect.DeepEqual(paths, want) {
		t.Fatalf("paths = %v, want %v", paths, want)
	}

	checkDirFinding(t, byRel(t, got, repo.Dir, ".aider.tags.cache.v3"), repo.Dir)
	if file := byRel(t, got, repo.Dir, ".aider.input.history"); file.Kind != findings.KindFile || file.SizeBytes != int64(len("data\n")) {
		t.Errorf("file finding %+v", file)
	}
}

// checkDirFinding asserts every field of the finding of the aider tag cache.
func checkDirFinding(t *testing.T, f findings.Finding, repoDir string) {
	t.Helper()
	if f.Detector != Name || f.Kind != findings.KindDir || f.Tool != "aider" || f.AgeDays != 100 || f.SizeBytes <= 0 {
		t.Errorf("unexpected dir finding %+v", f)
	}
	if f.ID != findings.NewID(Name, findings.KindDir, f.Path, "") {
		t.Errorf("id = %q", f.ID)
	}
	if f.Scope.Type != findings.ScopeRepo || f.Scope.Path != repoDir {
		t.Errorf("scope = %+v", f.Scope)
	}
	if f.Confidence != findings.ConfidenceHigh || f.Meta["catalog_tool"] != "aider" || f.Meta["pattern"] != ".aider.tags.cache.v*" {
		t.Errorf("confidence/meta = %q %v", f.Confidence, f.Meta)
	}
	if f.LastModified == nil || !f.LastModified.Equal(daysAgo(100)) {
		t.Errorf("last modified = %v, want %v", f.LastModified, daysAgo(100))
	}
	if f.SuggestedAction.Type != findings.ActionTrash || f.SuggestedAction.Command != "" || f.SuggestedAction.Reason == "" {
		t.Errorf("action = %+v", f.SuggestedAction)
	}
	checkEvidence(t, f, ".aider.tags.cache.v*")
	if len(f.RiskFlags) != 0 {
		t.Errorf("flags = %v, want none", f.RiskFlags)
	}
}

// checkEvidence asserts the matches_pattern and age evidence contract.
func checkEvidence(t *testing.T, f findings.Finding, pattern string) {
	t.Helper()
	codes := map[string]findings.Evidence{}
	for _, e := range f.Evidence {
		codes[e.Code] = e
	}
	mp, ok := codes["matches_pattern"]
	if !ok || mp.Value != pattern || !strings.Contains(mp.Message, pattern) || !strings.Contains(mp.Message, "Aider") {
		t.Errorf("matches_pattern evidence = %+v", mp)
	}
	age, ok := codes["age"]
	if !ok || age.Value != f.AgeDays {
		t.Errorf("age evidence = %+v (age_days %d)", age, f.AgeDays)
	}
}

func TestProtectWins(t *testing.T) {
	cases := []struct {
		name     string
		patterns []string
		files    []string
		want     []string
	}{
		{
			name:     "protected files are never reported",
			patterns: []string{".aider.conf.yml", "CLAUDE.md", ".claude/settings.json", "settings.json", ".claude/commands"},
			files:    []string{".aider.conf.yml", "CLAUDE.md", ".claude/settings.json", ".claude/commands/fix.md"},
			want:     []string{},
		},
		{
			name:     "a directory containing a protected path is dropped",
			patterns: []string{".claude"},
			files:    []string{".claude/settings.json", ".claude/projects/x.txt"},
			want:     []string{},
		},
		{
			name:     "a candidate below a protected path is dropped",
			patterns: []string{"cmd-*.md"},
			files:    []string{".claude/commands/cmd-1.md", "docs/cmd-2.md"},
			want:     []string{"docs/cmd-2.md"},
		},
		{
			name:     "a sibling of protected config is reported",
			patterns: []string{".claude"},
			files:    []string{".claude/projects/x.txt"},
			want:     []string{".claude"},
		},
		{
			name:     "protection applies at any depth inside a matched directory",
			patterns: []string{".scratch"},
			files:    []string{".scratch/a/b/CLAUDE.md", ".scratch/other.txt"},
			want:     []string{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sandbox(t)
			repo := testutil.NewRepo(t)
			for _, f := range tc.files {
				put(t, repo.Dir, f, 100)
			}
			oldDirs(t, repo.Dir)
			env := newEnv(t, cfgWith(extraTool("sweepy", 0, tc.patterns...)), repo.Dir)
			got := relPaths(t, mustScan(t, env, repoTarget(repo.Dir)), repo.Dir)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("paths = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestMatchedDirectoryWithGitIsDropped(t *testing.T) {
	sandbox(t)
	repo := testutil.NewRepo(t)
	// A nested repository and a linked worktree (a .git file) below one
	// scratch directory; a sibling scratch directory is plain.
	put(t, repo.Dir, ".scratch-a/inner/.git/HEAD", 100)
	put(t, repo.Dir, ".scratch-a/inner/code.go", 100)
	put(t, repo.Dir, ".scratch-b/wt/.git", 100)
	put(t, repo.Dir, ".scratch-b/wt/code.go", 100)
	put(t, repo.Dir, ".scratch-c/notes.txt", 100)
	oldDirs(t, repo.Dir)

	env := newEnv(t, cfgWith(extraTool("scratchy", 0, ".scratch-*")), repo.Dir)
	got := relPaths(t, mustScan(t, env, repoTarget(repo.Dir)), repo.Dir)
	if want := []string{".scratch-c"}; !reflect.DeepEqual(got, want) {
		t.Errorf("paths = %v, want %v", got, want)
	}
}

func TestNestedRepoIsSkippedAsTarget(t *testing.T) {
	sandbox(t)
	repo := testutil.NewRepo(t)
	put(t, repo.Dir, "vendored/.git", 100)
	put(t, repo.Dir, "vendored/.scratch-x", 100)
	put(t, repo.Dir, "own/.scratch-y", 100)
	oldDirs(t, repo.Dir)
	env := newEnv(t, cfgWith(extraTool("scratchy", 0, ".scratch-*")), repo.Dir)
	got := relPaths(t, mustScan(t, env, repoTarget(repo.Dir)), repo.Dir)
	if want := []string{"own/.scratch-y"}; !reflect.DeepEqual(got, want) {
		t.Errorf("paths = %v, want %v", got, want)
	}
}

func TestExcludes(t *testing.T) {
	cases := []struct {
		name string
		// setup returns the configuration and the project directory.
		setup func(t *testing.T, root, proj string) *config.Config
		want  []string
	}{
		{
			name: "root exclude, relative to the root",
			setup: func(t *testing.T, root, proj string) *config.Config {
				cfg := cfgWith(extraTool("scratchy", 0, ".scratch-*"))
				cfg.Roots = []config.Root{{Path: root, Exclude: []string{"proj/skipped"}}}
				return cfg
			},
			want: []string{"kept/.scratch-a"},
		},
		{
			name: "root exclude by name at any depth",
			setup: func(t *testing.T, root, proj string) *config.Config {
				cfg := cfgWith(extraTool("scratchy", 0, ".scratch-*"))
				cfg.Roots = []config.Root{{Path: root, Exclude: []string{"skipped"}}}
				return cfg
			},
			want: []string{"kept/.scratch-a"},
		},
		{
			name: "repo exclude, relative to the target",
			setup: func(t *testing.T, root, proj string) *config.Config {
				data, err := json.Marshal(config.RepoConfig{Exclude: []string{"skipped"}})
				if err != nil {
					t.Fatal(err)
				}
				testutil.WriteFile(t, proj, config.RepoConfigFileName, string(data))
				return cfgWith(extraTool("scratchy", 0, ".scratch-*"))
			},
			want: []string{"kept/.scratch-a"},
		},
		{
			name: "no exclude reports both",
			setup: func(t *testing.T, root, proj string) *config.Config {
				return cfgWith(extraTool("scratchy", 0, ".scratch-*"))
			},
			want: []string{"kept/.scratch-a", "skipped/.scratch-b", "skipped/deeper/.scratch-c"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sandbox(t)
			root := testutil.ResolvedTempDir(t)
			proj := filepath.Join(root, "proj")
			put(t, proj, "kept/.scratch-a", 100)
			put(t, proj, "skipped/.scratch-b", 100)
			put(t, proj, "skipped/deeper/.scratch-c", 100)
			oldDirs(t, proj)
			cfg := tc.setup(t, root, proj)
			env := newEnv(t, cfg, root)
			got := relPaths(t, mustScan(t, env, projectTarget(root, proj)), proj)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("paths = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestAgeAndSizeFilters covers the precedence entry > detector > global for
// the minimum age and the global minimum size.
func TestAgeAndSizeFilters(t *testing.T) {
	ptr := func(n int) *int { return &n }
	cases := []struct {
		name     string
		entryAge *int
		detAge   *int
		global   int
		fileAge  int
		minSize  int64
		want     bool
	}{
		{"entry wins over detector (older than entry)", ptr(5), ptr(100), 200, 30, 0, true},
		{"entry wins over detector (younger than entry)", ptr(50), ptr(1), 0, 30, 0, false},
		{"detector wins over global (passes)", nil, ptr(10), 90, 30, 0, true},
		{"detector wins over global (fails)", nil, ptr(40), 0, 30, 0, false},
		{"global applies last (passes)", nil, nil, 14, 30, 0, true},
		{"global applies last (fails)", nil, nil, 60, 30, 0, false},
		{"entry zero means no age requirement", ptr(0), ptr(100), 100, 0, 0, true},
		{"min size filters small findings", ptr(0), nil, 0, 30, 1 << 20, false},
		{"min size keeps findings at the limit", ptr(0), nil, 0, 30, int64(len("data\n")), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sandbox(t)
			dir := testutil.ResolvedTempDir(t)
			put(t, dir, ".probe", tc.fileAge)
			oldDirs(t, dir)
			tool := extraTool("probe", 0, ".probe")
			tool.Entries[0].MinAgeDays = tc.entryAge
			cfg := cfgWith(tool)
			cfg.Detectors.AIArtifacts.MinAgeDays = tc.detAge
			cfg.Thresholds.MinAgeDays = tc.global
			cfg.Thresholds.MinSizeBytes = tc.minSize
			env := newEnv(t, cfg, dir)
			got := mustScan(t, env, projectTarget(dir, dir))
			if (len(got) == 1) != tc.want {
				t.Errorf("findings = %d, want reported=%v", len(got), tc.want)
			}
		})
	}
}

// TestFreshAgeForInPlaceWrites proves that a transcript appended to in place
// (which leaves the directory mtime alone) is dated by its real mtime and not
// by a stale cached record.
func TestFreshAgeForInPlaceWrites(t *testing.T) {
	sandbox(t)
	dir := testutil.ResolvedTempDir(t)
	log := put(t, dir, ".scratch-run/run.jsonl", 100)
	oldDirs(t, dir)
	env := newEnv(t, cfgWith(extraTool("scratchy", 5, ".scratch-*")), dir)

	// Warm the size cache with the old state of the directory.
	if got := mustScan(t, env, projectTarget(dir, dir)); len(got) != 1 {
		t.Fatalf("old run: got %d findings, want 1", len(got))
	}
	// The agent writes to the log in place: file mtime moves, the directory's
	// does not.
	testutil.SetMTime(t, log, daysAgo(1))
	if got := mustScan(t, env, projectTarget(dir, dir)); len(got) != 0 {
		t.Fatalf("in-place written run: got %d findings, want 0 (age must be fresh)", len(got))
	}
}

func TestTrackedFiles(t *testing.T) {
	sandbox(t)
	repo := testutil.NewRepo(t)
	tracked := repo.WriteFile(".aider.chat.history.md", "committed\n")
	repo.CommitAll("track history", testutil.BaseTime)
	testutil.SetMTime(t, tracked, daysAgo(100))
	put(t, repo.Dir, ".aider.input.history", 100)
	oldDirs(t, repo.Dir)

	for _, force := range []bool{false, true} {
		env := newEnv(t, config.Default(), repo.Dir)
		env.Force = force
		got := mustScan(t, env, repoTarget(repo.Dir))
		f := byRel(t, got, repo.Dir, ".aider.chat.history.md")
		if !hasFlag(f, findings.RiskTrackedFiles) {
			t.Fatalf("force=%v: flags = %v, want tracked_files", force, f.RiskFlags)
		}
		checkTrackedAction(t, f, force)
		clean := byRel(t, got, repo.Dir, ".aider.input.history")
		if clean.SuggestedAction.Type != findings.ActionTrash || hasFlag(clean, findings.RiskTrackedFiles) {
			t.Errorf("untracked sibling: %+v", clean)
		}
	}
}

// TestTrackedFilesWithForeignGitDir keeps a committed file flagged when the
// environment exports GIT_DIR/GIT_INDEX_FILE of another repository, which
// used to make ls-files read the foreign index.
func TestTrackedFilesWithForeignGitDir(t *testing.T) {
	sandbox(t)
	repo := testutil.NewRepo(t)
	tracked := repo.WriteFile(".aider.chat.history.md", "committed\n")
	repo.CommitAll("track history", testutil.BaseTime)
	testutil.SetMTime(t, tracked, daysAgo(100))
	oldDirs(t, repo.Dir)
	foreign := testutil.NewRepo(t)
	t.Setenv("GIT_DIR", filepath.Join(foreign.Dir, ".git"))
	t.Setenv("GIT_INDEX_FILE", filepath.Join(foreign.Dir, ".git", "index"))

	env := newEnv(t, config.Default(), repo.Dir)
	f := byRel(t, mustScan(t, env, repoTarget(repo.Dir)), repo.Dir, ".aider.chat.history.md")
	if !hasFlag(f, findings.RiskTrackedFiles) {
		t.Fatalf("flags = %v, want tracked_files", f.RiskFlags)
	}
}

// checkTrackedAction asserts the action of a finding that has tracked_files:
// none without --force, trash with the "forced" reason with it.
func checkTrackedAction(t *testing.T, f findings.Finding, force bool) {
	t.Helper()
	a := f.SuggestedAction
	if force {
		if a.Type != findings.ActionTrash || a.Reason != "forced: tracked_files" || a.Command != "" {
			t.Errorf("forced action = %+v", a)
		}
		return
	}
	if a.Type != findings.ActionNone || !strings.Contains(a.Reason, "tracked_files") {
		t.Errorf("unforced action = %+v", a)
	}
}

func TestTrackedFileBelowMatchedDirectory(t *testing.T) {
	sandbox(t)
	repo := testutil.NewRepo(t)
	p := repo.WriteFile(".scratch-t/keep.txt", "committed\n")
	repo.CommitAll("track scratch file", testutil.BaseTime)
	testutil.SetMTime(t, p, daysAgo(100))
	put(t, repo.Dir, ".scratch-t/other.txt", 100)
	oldDirs(t, repo.Dir)
	env := newEnv(t, cfgWith(extraTool("scratchy", 0, ".scratch-*")), repo.Dir)
	f := byRel(t, mustScan(t, env, repoTarget(repo.Dir)), repo.Dir, ".scratch-t")
	if !hasFlag(f, findings.RiskTrackedFiles) || f.SuggestedAction.Type != findings.ActionNone {
		t.Errorf("dir with a tracked file: flags %v action %+v", f.RiskFlags, f.SuggestedAction)
	}
}

func TestGitignoredFlag(t *testing.T) {
	sandbox(t)
	repo := testutil.NewRepo(t)
	repo.WriteFile(".gitignore", ".aider.chat.history.md\n.aider.tags.cache.v*/\n.scratch-*\n")
	repo.CommitAll("ignore", testutil.BaseTime)
	put(t, repo.Dir, ".aider.chat.history.md", 100)
	put(t, repo.Dir, ".aider.input.history", 100)
	put(t, repo.Dir, ".aider.tags.cache.v3/tags", 100)
	// More ignored entries than fit one check-ignore chunk.
	const many = ignoreChunk + 20
	for i := 0; i < many; i++ {
		put(t, repo.Dir, ".scratch-"+strings.Repeat("x", 1)+string(rune('a'+i%26))+string(rune('a'+i/26)), 100)
	}
	oldDirs(t, repo.Dir)

	env := newEnv(t, cfgWith(extraTool("scratchy", 0, ".scratch-*")), repo.Dir)
	got := mustScan(t, env, repoTarget(repo.Dir))

	ignored := map[string]bool{".aider.chat.history.md": true, ".aider.tags.cache.v3": true, ".aider.input.history": false}
	for rel, want := range ignored {
		f := byRel(t, got, repo.Dir, rel)
		if hasFlag(f, findings.RiskGitignored) != want {
			t.Errorf("%s: gitignored = %v, want %v", rel, !want, want)
		}
		if f.SuggestedAction.Type != findings.ActionTrash {
			t.Errorf("%s: gitignored is informational, action = %+v", rel, f.SuggestedAction)
		}
	}
	count := 0
	for _, f := range got {
		if strings.HasPrefix(filepath.Base(f.Path), ".scratch-") {
			count++
			if !hasFlag(f, findings.RiskGitignored) {
				t.Errorf("%s: not flagged gitignored", f.Path)
			}
		}
	}
	if count != many {
		t.Errorf("scratch findings = %d, want %d", count, many)
	}
}

func TestSymlinkEntry(t *testing.T) {
	sandbox(t)
	repo := testutil.NewRepo(t)
	outside := testutil.ResolvedTempDir(t)
	secret := testutil.WriteFile(t, outside, "secret.txt", "do not touch\n")
	symlinkOrSkip(t, secret, filepath.Join(repo.Dir, "link-out"))

	// The guard allows only the repository: the link is inside, its target is
	// not. Only the link may ever be the subject.
	env := newEnv(t, cfgWith(extraTool("linky", 0, "link-*")), repo.Dir)
	got := mustScan(t, env, repoTarget(repo.Dir))
	if len(got) != 1 {
		t.Fatalf("findings = %d, want 1", len(got))
	}
	f := got[0]
	if f.Path != filepath.Join(repo.Dir, "link-out") || f.Kind != findings.KindFile || !hasFlag(f, findings.RiskSymlink) {
		t.Errorf("finding = %+v", f)
	}
	if b, err := os.ReadFile(secret); err != nil || string(b) != "do not touch\n" {
		t.Errorf("link target modified: %q %v", b, err)
	}
}

func TestPerToolToggleAndExtra(t *testing.T) {
	sandbox(t)
	repo := testutil.NewRepo(t)
	put(t, repo.Dir, ".aider.chat.history.md", 100)
	put(t, repo.Dir, "agent-notes.tmp", 100)
	oldDirs(t, repo.Dir)

	cases := []struct {
		name string
		edit func(*config.Config)
		want []string
	}{
		{"defaults", func(*config.Config) {}, []string{".aider.chat.history.md"}},
		{"tool disabled", func(c *config.Config) { c.Detectors.AIArtifacts.Tools = map[string]bool{"aider": false} }, []string{}},
		{"extra entry", func(c *config.Config) {
			c.Detectors.AIArtifacts.Extra = []config.CatalogTool{extraTool("notes", 0, "agent-notes.tmp")}
		},
			[]string{".aider.chat.history.md", "agent-notes.tmp"}},
		{"extra shorthand and disabled builtin", func(c *config.Config) {
			c.Detectors.AIArtifacts.Tools = map[string]bool{"aider": false}
			zero := 0
			c.Detectors.AIArtifacts.MinAgeDays = &zero
			c.Detectors.AIArtifacts.Extra = []config.CatalogTool{{ID: "notes", Name: "Notes", Project: []string{"*.tmp"}}}
		}, []string{"agent-notes.tmp"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Default()
			tc.edit(cfg)
			env := newEnv(t, cfg, repo.Dir)
			got := relPaths(t, mustScan(t, env, repoTarget(repo.Dir)), repo.Dir)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("paths = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestExtraWithInvalidEntryFailsTheTarget(t *testing.T) {
	sandbox(t)
	dir := testutil.ResolvedTempDir(t)
	env := newEnv(t, cfgWith(config.CatalogTool{ID: "bad", Name: "Bad"}), dir)
	if _, err := scan(t, env, projectTarget(dir, dir)); err == nil {
		t.Error("want an error for an extra without locations")
	}
}

func TestCaseInsensitiveMatching(t *testing.T) {
	if runtime.GOOS != "windows" && runtime.GOOS != "darwin" {
		t.Skip("case-insensitive matching applies to Windows and macOS filesystems only")
	}
	sandbox(t)
	dir := testutil.ResolvedTempDir(t)
	put(t, dir, ".AIDER.Chat.History.md", 100)
	oldDirs(t, dir)
	env := newEnv(t, config.Default(), dir)
	got := mustScan(t, env, projectTarget(dir, dir))
	if len(got) != 1 {
		t.Fatalf("findings = %d, want 1", len(got))
	}
	if f := got[0]; f.Meta["pattern"] != ".aider.chat.history.md" || f.Tool != "aider" {
		t.Errorf("finding = %+v", f)
	}
}

func TestMissingTargetIsAnError(t *testing.T) {
	sandbox(t)
	dir := filepath.Join(testutil.ResolvedTempDir(t), "gone")
	env := newEnv(t, config.Default(), filepath.Dir(dir))
	if _, err := scan(t, env, projectTarget(dir, dir)); err == nil {
		t.Error("want an error for a missing target directory")
	}
}

func TestContextCancellation(t *testing.T) {
	sandbox(t)
	repo := testutil.NewRepo(t)
	put(t, repo.Dir, ".aider.chat.history.md", 100)
	oldDirs(t, repo.Dir)
	env := newEnv(t, config.Default(), repo.Dir)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := scanCtx(ctx, env, repoTarget(repo.Dir)); err == nil {
		t.Error("want an error from a cancelled context")
	}
}

func TestUnsupportedTargetKindIsIgnored(t *testing.T) {
	env := &detect.Env{Config: config.Default(), Now: now}
	got, err := scanCtx(context.Background(), env, scope.Target{Kind: "bogus", Path: t.TempDir()})
	if err != nil || len(got) != 0 {
		t.Errorf("got %v, %v", got, err)
	}
}
