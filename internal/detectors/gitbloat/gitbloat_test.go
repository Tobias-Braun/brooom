package gitbloat

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/detect"
	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/gitx"
	"github.com/Tobias-Braun/brooom/internal/scope"
	"github.com/Tobias-Braun/brooom/internal/testutil"
)

// countingRunner counts git invocations by subcommand and can run a hook
// after each call.
type countingRunner struct {
	gitx.Runner
	countObjects atomic.Int32
	after        func(args []string)
}

func (c *countingRunner) Run(ctx context.Context, dir string, args ...string) (string, error) {
	if len(args) > 0 && args[0] == "count-objects" {
		c.countObjects.Add(1)
	}
	out, err := c.Runner.Run(ctx, dir, args...)
	if c.after != nil {
		c.after(args)
	}
	return out, err
}

// fixture bundles a scan environment for the detector.
type fixture struct {
	env    *detect.Env
	runner *countingRunner
	cfg    *config.Config
}

// tune are threshold overrides; a zero value keeps "never triggers".
type tune struct {
	loose, packs int
	reflog, blob int64
}

func newFixture(t *testing.T, tn tune, allowed ...string) *fixture {
	t.Helper()
	t.Setenv("BROOOM_HOME", testutil.ResolvedTempDir(t))
	home := testutil.ResolvedTempDir(t)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", home+"/.gitconfig-none")
	er, err := gitx.NewExecRunner()
	if err != nil {
		t.Skip("git not installed:", err)
	}
	runner := &countingRunner{Runner: er}
	cfg := config.Default()
	g := &cfg.Detectors.GitBloat
	g.LooseObjectsThreshold = 1 << 30
	g.PackCountThreshold = 1 << 30
	g.ReflogThresholdBytes = 1 << 40
	g.LargeBlobBytes = 0
	if tn.loose > 0 {
		g.LooseObjectsThreshold = tn.loose
	}
	if tn.packs > 0 {
		g.PackCountThreshold = tn.packs
	}
	if tn.reflog > 0 {
		g.ReflogThresholdBytes = tn.reflog
	}
	g.LargeBlobBytes = tn.blob
	guard, err := scope.NewGuard(allowed...)
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{
		runner: runner,
		cfg:    cfg,
		env: &detect.Env{
			Config: cfg,
			Git:    runner,
			Repos:  gitx.NewCache(runner),
			Guard:  guard,
			Now:    testutil.BaseTime.Add(48 * time.Hour),
		},
	}
}

func repoTarget(dir string) scope.Target {
	return scope.Target{Kind: scope.TargetRepo, Path: dir, Scope: findings.Scope{Type: findings.ScopeRepo, Path: dir}}
}

func (f *fixture) run(t *testing.T, d *Detector, target scope.Target) ([]findings.Finding, error) {
	t.Helper()
	return f.runCtx(t, context.Background(), d, target)
}

func (f *fixture) runCtx(t *testing.T, ctx context.Context, d *Detector, target scope.Target) ([]findings.Finding, error) {
	t.Helper()
	var out []findings.Finding
	err := d.Detect(ctx, f.env, target, func(x findings.Finding) { out = append(out, x) })
	return out, err
}

func byKind(fs []findings.Finding, k findings.Kind) []findings.Finding {
	var out []findings.Finding
	for _, f := range fs {
		if f.Kind == k {
			out = append(out, f)
		}
	}
	return out
}

func evidence(f findings.Finding, code string) (findings.Evidence, bool) {
	for _, e := range f.Evidence {
		if e.Code == code {
			return e, true
		}
	}
	return findings.Evidence{}, false
}

// looseObjects writes n distinct loose blobs without creating commits.
func looseObjects(t *testing.T, r *testutil.Repo, n int) {
	t.Helper()
	args := []string{"hash-object", "-w"}
	for i := 0; i < n; i++ {
		args = append(args, r.WriteFile(fmt.Sprintf("loose/f%03d.txt", i), fmt.Sprintf("loose object %d\n", i)))
	}
	r.Git(args...)
}

func TestRegistration(t *testing.T) {
	d, ok := detect.Get(Name)
	if !ok {
		t.Fatal("git-bloat not registered")
	}
	if d.Name() != "git-bloat" || d.Category() != detect.CategoryGit || d.Description() == "" {
		t.Errorf("metadata: %q %q %q", d.Name(), d.Category(), d.Description())
	}
}

func TestBelowThresholdsEmitsNothing(t *testing.T) {
	r := testutil.NewRepo(t)
	f := newFixture(t, tune{}, r.Dir)
	got, err := f.run(t, New(), repoTarget(r.Dir))
	if err != nil || len(got) != 0 {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestLooseObjectsFinding(t *testing.T) {
	r := testutil.NewRepo(t)
	looseObjects(t, r, 30)
	f := newFixture(t, tune{loose: 10}, r.Dir)
	got, err := f.run(t, New(), repoTarget(r.Dir))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 finding, got %d: %+v", len(got), got)
	}
	x := got[0]
	if x.Kind != findings.KindGitObjects || x.Detector != Name || x.Path != r.Dir {
		t.Errorf("finding: %+v", x)
	}
	if x.ID != findings.NewID(Name, findings.KindGitObjects, r.Dir, "") {
		t.Errorf("id %s", x.ID)
	}
	if x.Confidence != findings.ConfidenceMedium || x.SizeBytes <= 0 {
		t.Errorf("confidence %s size %d", x.Confidence, x.SizeBytes)
	}
	count, ok := evidence(x, "loose_object_count")
	if !ok || count.Value.(int64) < 30 {
		t.Errorf("loose_object_count: %+v", count)
	}
	size, _ := evidence(x, "loose_size_bytes")
	est, ok := evidence(x, "estimated_savings")
	if !ok || est.Value.(int64) != size.Value.(int64)/2 || x.SizeBytes != est.Value.(int64) {
		t.Errorf("estimate %+v of size %+v, finding %d", est, size, x.SizeBytes)
	}
	if _, ok := evidence(x, "garbage"); ok {
		t.Error("garbage evidence without garbage")
	}
	a := x.SuggestedAction
	wantPrune := f.cfg.Detectors.GitBloat.PruneExpire
	if a.Type != findings.ActionGitGC || a.Args["prune"] != wantPrune || a.Command != "git gc --prune="+wantPrune {
		t.Errorf("action: %+v", a)
	}
	for _, s := range []string{"gc.reflogExpire", "gc.reflogExpireUnreachable", "90", "30", "recover deleted branches"} {
		if !strings.Contains(a.Reason, s) {
			t.Errorf("reason lacks %q: %s", s, a.Reason)
		}
	}
	if x.LastModified == nil {
		t.Error("last_modified not set from objects dir")
	}
}

func TestLooseObjectsUsesConfiguredPrune(t *testing.T) {
	r := testutil.NewRepo(t)
	looseObjects(t, r, 20)
	f := newFixture(t, tune{loose: 5}, r.Dir)
	f.cfg.Detectors.GitBloat.PruneExpire = "1.day.ago"
	got, err := f.run(t, New(), repoTarget(r.Dir))
	if err != nil || len(got) != 1 {
		t.Fatalf("%v %v", got, err)
	}
	if got[0].SuggestedAction.Args["prune"] != "1.day.ago" || got[0].SuggestedAction.Command != "git gc --prune=1.day.ago" {
		t.Errorf("action: %+v", got[0].SuggestedAction)
	}
}

// packSince writes one new pack holding everything reachable from HEAD but
// not from prev (all of it when prev is empty) and drops the loose copies.
// Unlike "git repack -d", whose consolidation behaviour differs between git
// versions, pack-objects always yields exactly one additional pack.
func packSince(t *testing.T, r *testutil.Repo, prev string) {
	t.Helper()
	in := "HEAD\n"
	if prev != "" {
		in += "^" + prev + "\n"
	}
	cmd := exec.Command("git", "pack-objects", "--revs", "-q", ".git/objects/pack/pack")
	cmd.Dir = r.Dir
	cmd.Env = r.Env(testutil.BaseTime)
	cmd.Stdin = strings.NewReader(in)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git pack-objects: %v\n%s", err, out)
	}
	r.Git("prune-packed")
}

func TestPacksFinding(t *testing.T) {
	r := testutil.NewRepo(t)
	// Newer git versions run auto maintenance (which may consolidate packs)
	// after commits; it must not undo the fragmentation built here.
	r.Git("config", "gc.auto", "0")
	r.Git("config", "maintenance.auto", "false")
	prev := ""
	for i := 0; i < 4; i++ {
		r.WriteFile(fmt.Sprintf("f%d.txt", i), strings.Repeat(fmt.Sprintf("content %d\n", i), 200))
		r.CommitAll(fmt.Sprintf("c%d", i), testutil.BaseTime.Add(time.Duration(i)*time.Hour))
		packSince(t, r, prev)
		prev = r.Head()
	}
	f := newFixture(t, tune{packs: 2}, r.Dir)
	got, err := f.run(t, New(), repoTarget(r.Dir))
	if err != nil {
		t.Fatal(err)
	}
	packs := byKind(got, findings.KindGitPacks)
	if len(packs) != 1 {
		t.Fatalf("want 1 packs finding in %+v (count-objects: %s)", got, r.Git("count-objects", "-v"))
	}
	x := packs[0]
	n, _ := evidence(x, "pack_count")
	size, ok := evidence(x, "pack_size_bytes")
	est, ok2 := evidence(x, "estimated_savings")
	if n.Value.(int64) < 3 || !ok || !ok2 || est.Value.(int64) != size.Value.(int64)/10 || x.SizeBytes != est.Value.(int64) {
		t.Errorf("evidence: %+v", x.Evidence)
	}
	if x.SuggestedAction.Type != findings.ActionGitGC || !strings.Contains(x.SuggestedAction.Reason, "gc.reflogExpire") {
		t.Errorf("action: %+v", x.SuggestedAction)
	}
}

func TestReflogFinding(t *testing.T) {
	r := testutil.NewRepo(t)
	for i := 0; i < 25; i++ {
		r.WriteFile("a.txt", fmt.Sprintf("v%d\n", i))
		r.CommitAll(fmt.Sprintf("commit %d", i), testutil.BaseTime.Add(time.Duration(i)*time.Minute))
	}
	f := newFixture(t, tune{reflog: 500}, r.Dir)
	f.cfg.Detectors.GitBloat.ReflogExpire = "30.days.ago"
	got, err := f.run(t, New(), repoTarget(r.Dir))
	if err != nil {
		t.Fatal(err)
	}
	rl := byKind(got, findings.KindGitReflog)
	if len(rl) != 1 {
		t.Fatalf("want reflog finding in %+v", got)
	}
	x := rl[0]
	size, ok := evidence(x, "reflog_size_bytes")
	_, ub := evidence(x, "upper_bound")
	if !ok || !ub || x.SizeBytes != size.Value.(int64) || x.SizeBytes < 500 {
		t.Errorf("size %d evidence %+v", x.SizeBytes, x.Evidence)
	}
	a := x.SuggestedAction
	if a.Type != findings.ActionGitReflogExpire || a.Args["expire"] != "30.days.ago" ||
		a.Command != "git reflog expire --expire=30.days.ago --all" ||
		!strings.Contains(a.Reason, "can no longer be used to recover deleted branches or reset commits") {
		t.Errorf("action: %+v", a)
	}
	if x.LastModified == nil {
		t.Error("last_modified missing")
	}
}

func TestReflogIncludesLinkedWorktreeLogs(t *testing.T) {
	r := testutil.NewRepo(t)
	wt := r.AddWorktree("wt", "feat")
	for i := 0; i < 5; i++ {
		testutil.WriteFile(t, wt, "w.txt", fmt.Sprintf("v%d\n", i))
		cmd := exec.Command("git", "-C", wt, "-c", "user.name=t", "-c", "user.email=t@t.invalid", "commit", "-q", "-a", "--allow-empty", "-m", "x")
		cmd.Env = r.Env(testutil.BaseTime)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
	}
	dirs := reflogDirs(mustCommon(t, r))
	if len(dirs) != 2 {
		t.Fatalf("want main + worktree logs, got %v", dirs)
	}
}

func mustCommon(t *testing.T, r *testutil.Repo) string {
	t.Helper()
	er, err := gitx.NewExecRunner()
	if err != nil {
		t.Skip(err)
	}
	c, err := gitx.CommonDir(context.Background(), er, r.Dir)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func largeBlobRepo(t *testing.T) *testutil.Repo {
	t.Helper()
	r := testutil.NewRepo(t)
	r.WriteFile("data/big.bin", strings.Repeat("0123456789abcdef", 1024)) // 16 KiB
	r.WriteFile("data/mid.bin", strings.Repeat("z", 6000))
	r.CommitAll("add blobs", testutil.BaseTime.Add(time.Hour))
	return r
}

func TestLargeBlobFindings(t *testing.T) {
	r := largeBlobRepo(t)
	f := newFixture(t, tune{blob: 4096}, r.Dir)
	got, err := f.run(t, New(), repoTarget(r.Dir))
	if err != nil {
		t.Fatal(err)
	}
	blobs := byKind(got, findings.KindGitLargeBlob)
	if len(blobs) != 2 {
		t.Fatalf("want 2 blobs, got %+v", got)
	}
	sha := r.Git("rev-parse", "HEAD:data/big.bin")
	x := blobs[0]
	if x.Ref != sha || x.Meta["blob_path"] != "data/big.bin" || x.Meta["blob_size"] != "16384" {
		t.Errorf("largest blob: %+v", x)
	}
	if blobs[1].Meta["blob_size"] != "6000" {
		t.Errorf("order: %+v", blobs[1].Meta)
	}
	if x.SuggestedAction.Type != findings.ActionNone || !strings.Contains(x.SuggestedAction.Reason, "git filter-repo") ||
		!strings.Contains(x.SuggestedAction.Reason, "out of scope") {
		t.Errorf("action: %+v", x.SuggestedAction)
	}
	if x.SizeBytes != 0 || x.Confidence != findings.ConfidenceLow || x.Path != r.Dir {
		t.Errorf("finding: %+v", x)
	}
	if x.ID != findings.NewID(Name, findings.KindGitLargeBlob, r.Dir, sha) {
		t.Errorf("id %s", x.ID)
	}
	if _, ok := evidence(x, "truncated"); ok {
		t.Error("unexpected truncated evidence")
	}
}

func TestLargeBlobCapAndTruncated(t *testing.T) {
	r := testutil.NewRepo(t)
	for i := 0; i < maxBlobFindings+3; i++ {
		r.WriteFile(fmt.Sprintf("b%02d.bin", i), strings.Repeat(string(rune('a'+i%26)), 2000+i*10))
	}
	r.CommitAll("many", testutil.BaseTime.Add(time.Hour))
	f := newFixture(t, tune{blob: 1000}, r.Dir)
	got, err := f.run(t, New(), repoTarget(r.Dir))
	if err != nil {
		t.Fatal(err)
	}
	blobs := byKind(got, findings.KindGitLargeBlob)
	if len(blobs) != maxBlobFindings {
		t.Fatalf("want %d, got %d", maxBlobFindings, len(blobs))
	}
	tr, ok := evidence(blobs[0], "truncated")
	if !ok || tr.Value.(int) != maxBlobFindings+3 {
		t.Errorf("truncated: %+v", tr)
	}
	for i := 1; i < len(blobs); i++ {
		if blobs[i-1].Meta["blob_size"] < blobs[i].Meta["blob_size"] && len(blobs[i-1].Meta["blob_size"]) == len(blobs[i].Meta["blob_size"]) {
			t.Errorf("not sorted at %d", i)
		}
	}
}

func TestBlobScanDisabledAtZero(t *testing.T) {
	r := largeBlobRepo(t)
	f := newFixture(t, tune{}, r.Dir) // blob = 0
	got, err := f.run(t, New(), repoTarget(r.Dir))
	if err != nil || len(got) != 0 {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestBlobScanTimeoutKeepsOtherFindings(t *testing.T) {
	r := largeBlobRepo(t)
	looseObjects(t, r, 20)
	f := newFixture(t, tune{loose: 5, blob: 4096}, r.Dir)
	d := New()
	d.blobTimeout = time.Nanosecond
	got, err := f.run(t, d, repoTarget(r.Dir))
	// The gap is reported as a (non-fatal) scan error, never silently.
	if err == nil || !strings.Contains(err.Error(), "large blob scan") || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("timeout must be reported, got %v", err)
	}
	if len(byKind(got, findings.KindGitLargeBlob)) != 0 {
		t.Errorf("blob findings after timeout: %+v", got)
	}
	if len(byKind(got, findings.KindGitObjects)) != 1 {
		t.Errorf("other findings lost: %+v", got)
	}
}

// rescan drops the per-scan memoization so the next run behaves like a new
// brooom invocation that shares only the on-disk cache.
func (f *fixture) rescan() { f.env.Repos = gitx.NewCache(f.runner) }

// tamperBlobCache marks the cached scan so a later run shows whether it was
// served from disk (the marker survives) or rescanned (it is gone). The
// history walk itself runs through gitx.Pipe and cannot be counted with the
// runner.
func tamperBlobCache(t *testing.T, cacheDir string) {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join(cacheDir, "gitbloat-blobs-*.json"))
	if len(files) != 1 {
		t.Fatalf("want one blob cache file, got %v", files)
	}
	data, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	data = []byte(strings.ReplaceAll(string(data), "data/big.bin", "from/cache.bin"))
	if err := os.WriteFile(files[0], data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func blobPaths(fs []findings.Finding) []string {
	var out []string
	for _, x := range byKind(fs, findings.KindGitLargeBlob) {
		out = append(out, x.Meta["blob_path"])
	}
	return out
}

func TestBlobScanCachedOnDisk(t *testing.T) {
	r := largeBlobRepo(t)
	f := newFixture(t, tune{blob: 4096}, r.Dir)
	f.env.CacheDir = testutil.ResolvedTempDir(t)
	first, err := f.run(t, New(), repoTarget(r.Dir))
	if err != nil || len(blobPaths(first)) != 2 || blobPaths(first)[0] != "data/big.bin" {
		t.Fatalf("first run: %v, %v", err, blobPaths(first))
	}
	tamperBlobCache(t, f.env.CacheDir)

	f.rescan()
	second, err := f.run(t, New(), repoTarget(r.Dir))
	if err != nil || blobPaths(second)[0] != "from/cache.bin" {
		t.Fatalf("unchanged repo must be served from the cache: %v, %v", err, blobPaths(second))
	}

	t.Run("new commit invalidates", func(t *testing.T) {
		r.WriteFile("data/new.bin", strings.Repeat("n", 9000))
		r.CommitAll("more", testutil.BaseTime.Add(2*time.Hour))
		f.rescan()
		got, err := f.run(t, New(), repoTarget(r.Dir))
		if err != nil || len(blobPaths(got)) != 3 || blobPaths(got)[1] != "data/new.bin" {
			t.Fatalf("%v, %v", err, blobPaths(got))
		}
		tamperBlobCache(t, f.env.CacheDir)
	})
	t.Run("threshold invalidates", func(t *testing.T) {
		f.cfg.Detectors.GitBloat.LargeBlobBytes = 8000
		f.rescan()
		got, err := f.run(t, New(), repoTarget(r.Dir))
		if err != nil || len(blobPaths(got)) != 2 || blobPaths(got)[1] != "data/new.bin" {
			t.Fatalf("%v, %v", err, blobPaths(got))
		}
	})
}

func TestBlobScanFailureIsNotCached(t *testing.T) {
	r := largeBlobRepo(t)
	f := newFixture(t, tune{blob: 4096}, r.Dir)
	f.env.CacheDir = testutil.ResolvedTempDir(t)
	slow := New()
	slow.blobTimeout = time.Nanosecond
	if _, err := f.run(t, slow, repoTarget(r.Dir)); err == nil {
		t.Fatal("timeout must be reported")
	}
	f.rescan()
	got, err := f.run(t, New(), repoTarget(r.Dir))
	if err != nil || len(byKind(got, findings.KindGitLargeBlob)) != 2 {
		t.Fatalf("a failed scan must not poison the cache: %v %+v", err, got)
	}
}

func TestBlobCacheRejectsBadFiles(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "c.json")
	good := `{"version":1,"key":"k","total":1,"blobs":[{"sha":"abc","size":5000}]}`
	for name, tc := range map[string]struct {
		content string
		want    bool
	}{
		"valid":       {good, true},
		"other key":   {strings.Replace(good, `"k"`, `"z"`, 1), false},
		"old version": {strings.Replace(good, `"version":1`, `"version":0`, 1), false},
		"below min":   {strings.Replace(good, "5000", "10", 1), false},
		"empty sha":   {strings.Replace(good, `"abc"`, `""`, 1), false},
		"corrupt":     {"{nope", false},
	} {
		if err := os.WriteFile(file, []byte(tc.content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, ok := loadBlobCache(file, "k", 4096); ok != tc.want {
			t.Errorf("%s: ok = %v, want %v", name, ok, tc.want)
		}
	}
}

func TestParentCancelReturnsContextError(t *testing.T) {
	r := largeBlobRepo(t)
	looseObjects(t, r, 20)
	t.Run("already cancelled", func(t *testing.T) {
		f := newFixture(t, tune{loose: 5, blob: 4096}, r.Dir)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := f.runCtx(t, ctx, New(), repoTarget(r.Dir))
		if !errors.Is(err, context.Canceled) || !errors.Is(err, ctx.Err()) {
			t.Fatalf("want ctx.Err(), got %v", err)
		}
	})
	t.Run("cancelled before the blob scan", func(t *testing.T) {
		f := newFixture(t, tune{loose: 5, blob: 4096}, r.Dir)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		f.runner.after = func(args []string) {
			if args[0] == "count-objects" {
				cancel()
			}
		}
		_, err := f.runCtx(t, ctx, New(), repoTarget(r.Dir))
		if !errors.Is(err, ctx.Err()) || err == nil {
			t.Fatalf("want ctx.Err(), got %v", err)
		}
	})
}

func TestLinkedWorktreeDedup(t *testing.T) {
	r := testutil.NewRepo(t)
	looseObjects(t, r, 20)
	r.WriteFile("big.bin", strings.Repeat("q", 9000))
	r.CommitAll("big", testutil.BaseTime.Add(time.Hour))
	wt := r.AddWorktree("linked", "feat")
	f := newFixture(t, tune{loose: 5, blob: 4096}, r.Dir, wt)
	d := New()

	main, err := f.run(t, d, repoTarget(r.Dir))
	if err != nil {
		t.Fatal(err)
	}
	linked, err := f.run(t, d, repoTarget(wt))
	if err != nil {
		t.Fatal(err)
	}
	if len(main) == 0 || len(main) != len(linked) {
		t.Fatalf("main %d linked %d", len(main), len(linked))
	}
	for i := range main {
		if main[i].ID != linked[i].ID || main[i].Path != r.Dir || linked[i].Path != r.Dir {
			t.Errorf("finding %d differs: %s/%s path %s", i, main[i].ID, linked[i].ID, linked[i].Path)
		}
	}
	if n := f.runner.countObjects.Load(); n != 1 {
		t.Errorf("count-objects ran %d times, want 1", n)
	}
}

func TestLinkedWorktreeWithMainOutsideScopeIsSilent(t *testing.T) {
	r := testutil.NewRepo(t)
	looseObjects(t, r, 20)
	wt := r.AddWorktree("linked", "feat")
	f := newFixture(t, tune{loose: 5}, wt) // main checkout not allowed
	got, err := f.run(t, New(), repoTarget(wt))
	if err != nil || len(got) != 0 {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestSkips(t *testing.T) {
	r := testutil.NewRepo(t)
	looseObjects(t, r, 20)
	plain := testutil.ResolvedTempDir(t)
	tests := []struct {
		name   string
		target scope.Target
		mutate func(*fixture)
	}{
		{name: "project target", target: scope.Target{Kind: scope.TargetProject, Path: r.Dir}},
		{name: "user target", target: scope.Target{Kind: scope.TargetUser, Path: r.Dir}},
		{name: "not a repository", target: repoTarget(plain)},
		{name: "disabled", target: repoTarget(r.Dir), mutate: func(f *fixture) { f.cfg.Detectors.GitBloat.Enabled = false }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, tune{loose: 5}, r.Dir, plain)
			if tt.mutate != nil {
				tt.mutate(f)
			}
			got, err := f.run(t, New(), tt.target)
			if err != nil || len(got) != 0 {
				t.Fatalf("got %v, %v", got, err)
			}
		})
	}
}

func TestBareRepoIsSkipped(t *testing.T) {
	dir := testutil.ResolvedTempDir(t)
	f := newFixture(t, tune{loose: 1}, dir)
	cmd := exec.Command("git", "init", "-q", "--bare", dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	got, err := f.run(t, New(), repoTarget(dir))
	if err != nil || len(got) != 0 {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestRepoWithoutCommits(t *testing.T) {
	dir := testutil.ResolvedTempDir(t)
	f := newFixture(t, tune{blob: 1}, dir)
	cmd := exec.Command("git", "init", "-q", "-b", "main", dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	got, err := f.run(t, New(), repoTarget(dir))
	if err != nil || len(got) != 0 {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestShallowCloneAndAlternates(t *testing.T) {
	src := largeBlobRepo(t)
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"shallow", []string{"clone", "-q", "--depth", "1", "file://" + toSlash(src.Dir)}},
		{"alternates", []string{"clone", "-q", "--shared", src.Dir}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parent := testutil.ResolvedTempDir(t)
			dst := parent + string(os.PathSeparator) + "clone"
			cmd := exec.Command("git", append(tc.args, dst)...)
			cmd.Env = src.Env(testutil.BaseTime)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("clone: %v\n%s", err, out)
			}
			f := newFixture(t, tune{blob: 4096}, parent)
			got, err := f.run(t, New(), repoTarget(dst))
			if err != nil {
				t.Fatal(err)
			}
			if tc.name == "shallow" && len(byKind(got, findings.KindGitLargeBlob)) != 2 {
				t.Errorf("shallow clone blobs: %+v", got)
			}
		})
	}
}

func toSlash(p string) string { return strings.ReplaceAll(p, string(os.PathSeparator), "/") }

func TestMissingGitBinary(t *testing.T) {
	r := testutil.NewRepo(t)
	f := newFixture(t, tune{loose: 1}, r.Dir)
	f.env.Git = &gitx.ExecRunner{Path: "git-does-not-exist-brooom"}
	f.env.Repos = gitx.NewCache(f.env.Git)
	_, err := f.run(t, New(), repoTarget(r.Dir))
	if !errors.Is(err, gitx.ErrGitNotFound) {
		t.Fatalf("want ErrGitNotFound, got %v", err)
	}
}

func TestGarbageEvidence(t *testing.T) {
	r := testutil.NewRepo(t)
	looseObjects(t, r, 20)
	// A stray file in the pack directory is reported by count-objects as
	// garbage.
	r.WriteFile(".git/objects/pack/tmp_pack_junk", "junk")
	f := newFixture(t, tune{loose: 5}, r.Dir)
	got, err := f.run(t, New(), repoTarget(r.Dir))
	if err != nil || len(got) != 1 {
		t.Fatalf("%v %v", got, err)
	}
	if e, ok := evidence(got[0], "garbage"); !ok || e.Value.(int64) < 1 {
		t.Errorf("garbage evidence: %+v", got[0].Evidence)
	}
}

func TestParseBlobLine(t *testing.T) {
	tests := []struct {
		line string
		want blob
		ok   bool
	}{
		{"blob abc123 4096 dir/with space/file.bin", blob{"abc123", 4096, "dir/with space/file.bin"}, true},
		{"blob abc123 10 ", blob{"abc123", 10, ""}, true},
		{"blob abc123 10", blob{"abc123", 10, ""}, true},
		{"tree abc 10 dir", blob{}, false},
		{"commit abc 10 ", blob{}, false},
		{"abc123 missing", blob{}, false},
		{"blob abc nope x", blob{}, false},
		{"", blob{}, false},
	}
	for _, tt := range tests {
		got, ok := parseBlobLine(tt.line)
		if ok != tt.ok || got != tt.want {
			t.Errorf("%q: got %+v %v, want %+v %v", tt.line, got, ok, tt.want, tt.ok)
		}
	}
}

func TestHumanBytes(t *testing.T) {
	for n, want := range map[int64]string{0: "0 B", 1023: "1023 B", 1024: "1.0 KiB", 5 << 20: "5.0 MiB", 3 << 30: "3.0 GiB"} {
		if got := humanBytes(n); got != want {
			t.Errorf("%d: %q, want %q", n, got, want)
		}
	}
}
