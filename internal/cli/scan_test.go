package cli

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/detect"
	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/gitx"
	"github.com/Tobias-Braun/brooom/internal/scope"
	"github.com/Tobias-Braun/brooom/internal/testutil"
)

func needGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
}

func TestScanRepoMode(t *testing.T) {
	needGit(t)
	isolate(t)
	repo := testutil.NewRepo(t)
	t.Chdir(repo.Dir)
	d, rec := recordingFake(t, detect.CategoryFiles)

	for _, format := range []string{"table", "summary"} {
		code, out, errOut := runScanCmd(t, "scan", "-d", d.name, "-f", format)
		if code != ExitOK {
			t.Fatalf("%s: code %d, stderr %q", format, code, errOut)
		}
		if !strings.Contains(out, d.name) {
			t.Errorf("%s output does not mention the detector: %q", format, out)
		}
		if errOut != "" {
			t.Errorf("%s: unexpected stderr %q", format, errOut)
		}
	}
	if got := rec.paths(); len(got) != 2 || got[0] != repo.Dir || got[1] != repo.Dir {
		t.Errorf("scanned targets = %v, want the repo twice", got)
	}
	tg := rec.targets[0]
	wantScope := findings.Scope{Type: findings.ScopeRepo, Path: repo.Dir}
	if tg.Kind != scope.TargetRepo || tg.Scope != wantScope {
		t.Errorf("target = %+v, want repo target with scope %+v", tg, wantScope)
	}
	if len(rec.allowed) != 1 || rec.allowed[0] != repo.Dir {
		t.Errorf("guard allows %v, want only %s", rec.allowed, repo.Dir)
	}
}

func TestBareCommandScansLikeScan(t *testing.T) {
	needGit(t)
	isolate(t)
	repo := testutil.NewRepo(t)
	t.Chdir(repo.Dir)
	d, rec := recordingFake(t, detect.CategoryFiles)
	code, _, errOut := runScanCmd(t, "-d", d.name)
	if code != ExitOK || len(rec.paths()) != 1 {
		t.Fatalf("code %d, targets %v, stderr %q", code, rec.paths(), errOut)
	}
}

func TestScanFromSubdirectoryScansRepoRoot(t *testing.T) {
	needGit(t)
	isolate(t)
	repo := testutil.NewRepo(t)
	sub := filepath.Join(repo.Dir, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(sub)
	d, rec := recordingFake(t, detect.CategoryFiles)
	if code, _, errOut := runScanCmd(t, "scan", "-d", d.name); code != ExitOK {
		t.Fatalf("code %d, stderr %q", code, errOut)
	}
	if got := rec.paths(); len(got) != 1 || got[0] != repo.Dir {
		t.Errorf("targets = %v, want [%s]", got, repo.Dir)
	}
}

// TestScanFromLinkedWorktreeCoversTheRepository pins the scope of a run from
// a linked worktree: the whole repository, so the linked worktree and the main
// worktree are both repo targets and allowed locations, and the main worktree
// is still the repository metadata location the git detectors resolve.
func TestScanFromLinkedWorktreeCoversTheRepository(t *testing.T) {
	needGit(t)
	isolate(t)
	repo := testutil.NewRepo(t)
	wt := repo.AddWorktree("agent-wt", "feat/agent")
	t.Chdir(wt)

	var meta, general error
	var resolvedMeta string
	d := registerFake(t, detect.CategoryGit, nil)
	rec := &recorder{}
	d.fn = func(_ context.Context, env *detect.Env, tg scope.Target, emit func(findings.Finding)) error {
		rec.record(env, tg)
		resolvedMeta, meta = env.Guard.ResolveRepoMeta(repo.Dir)
		_, general = env.Guard.Resolve(filepath.Join(repo.Dir, "README.md"))
		emitAt(d, tg, emit, "")
		return nil
	}

	code, _, errOut := runScanCmd(t, "scan", "-d", d.name)
	if code != ExitOK {
		t.Fatalf("code %d, stderr %q", code, errOut)
	}
	if meta != nil || resolvedMeta != repo.Dir || general != nil {
		t.Errorf("main worktree: meta %q, %v; general %v", resolvedMeta, meta, general)
	}
	got := rec.paths()
	sort.Strings(got)
	want := []string{repo.Dir, wt}
	sort.Strings(want)
	if !slices.Equal(got, want) {
		t.Errorf("targets = %v, want the linked and the main worktree %v", got, want)
	}
	if !slices.Contains(rec.allowed, wt) || !slices.Contains(rec.allowed, repo.Dir) {
		t.Errorf("guard allows %v, want %s and %s", rec.allowed, wt, repo.Dir)
	}
}

// countingRunner counts git calls and delegates or fails.
type countingRunner struct {
	mu    sync.Mutex
	calls [][]string
	out   string
	err   error
}

func (r *countingRunner) Run(_ context.Context, _ string, args ...string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, args)
	return r.out, r.err
}

func TestNormalRepoMakesNoWorktreeListCall(t *testing.T) {
	needGit(t)
	repo := testutil.NewRepo(t)
	runner := &countingRunner{}
	ts, err := repoTargets(context.Background(), runner, repo.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != 0 {
		t.Errorf("git was called in a normal repo: %v", runner.calls)
	}
	if len(ts.errs) != 0 || len(ts.allowed) != 1 {
		t.Errorf("targetSet = %+v", ts)
	}
}

func TestLinkedWorktreeListFailureIsScanError(t *testing.T) {
	needGit(t)
	repo := testutil.NewRepo(t)
	wt := repo.AddWorktree("wt", "feat/x")
	t.Chdir(wt)

	cases := map[string]gitRunnerFake{
		"git fails":      {err: errors.New("boom")},
		"empty listing":  {out: ""},
		"nil runner":     {nilRunner: true},
		"missing mainwt": {out: "worktree " + filepath.ToSlash(filepath.Join(wt, "gone"))},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			cwd, err := os.Getwd()
			if err != nil {
				t.Fatal(err)
			}
			ts, err := repoTargets(context.Background(), c.runner(), cwd)
			if err != nil {
				t.Fatal(err)
			}
			if len(ts.errs) != 1 || ts.errs[0].Path == "" {
				t.Fatalf("errs = %+v, want one scan error", ts.errs)
			}
			if len(ts.targets) != 1 || ts.targets[0].Path != wt || !slices.Equal(ts.allowed, []string{wt}) {
				t.Errorf("must continue with the linked worktree only: %+v", ts)
			}
		})
	}
}

type gitRunnerFake struct {
	out       string
	err       error
	nilRunner bool
}

func (g gitRunnerFake) runner() gitx.Runner {
	if g.nilRunner {
		return nil
	}
	return &countingRunner{out: g.out, err: g.err}
}

func TestLinkedWorktreeMainParsesPorcelain(t *testing.T) {
	out := "worktree /repo/main\nHEAD abc\nbranch refs/heads/main\n\nworktree /repo/linked\nHEAD def"
	got, err := linkedWorktreeMain(context.Background(), &countingRunner{out: out}, "/repo/linked")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.FromSlash("/repo/main"); got != want {
		t.Errorf("main worktree = %q, want %q", got, want)
	}
}

func TestScanOutsideRepoIsUsageError(t *testing.T) {
	isolate(t)
	t.Chdir(testutil.ResolvedTempDir(t))
	d, rec := recordingFake(t, detect.CategoryFiles)
	code, out, errOut := runScanCmd(t, "scan", "-d", d.name)
	if code != ExitUsage {
		t.Fatalf("code = %d, want %d (stderr %q)", code, ExitUsage, errOut)
	}
	for _, want := range []string{"not inside a git repository", "pass a folder", "brooom sweep ~/code"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("stderr %q lacks %q", errOut, want)
		}
	}
	if out != "" || len(rec.paths()) != 0 {
		t.Errorf("nothing may be scanned or printed: out=%q targets=%v", out, rec.paths())
	}
}

func TestScanPathWalksAFolder(t *testing.T) {
	isolate(t)
	root := testutil.ResolvedTempDir(t)
	fakeRepoDir(t, root, "alpha")
	fakeRepoDir(t, root, "beta")
	projectDir(t, root, "gamma")
	t.Chdir(testutil.ResolvedTempDir(t)) // outside any repo: the path decides
	d, rec := recordingFake(t, detect.CategoryFiles)

	code, out, errOut := runScanCmd(t, "scan", root, "-d", d.name)
	if code != ExitOK {
		t.Fatalf("code %d, stderr %q", code, errOut)
	}
	want := []string{
		filepath.Join(root, "alpha"),
		filepath.Join(root, "beta"),
		filepath.Join(root, "gamma"),
	}
	got := rec.paths()
	sort.Strings(got)
	if !slices.Equal(got, want) {
		t.Errorf("targets = %v, want %v", got, want)
	}
	for _, tg := range rec.targets {
		if tg.Scope != (findings.Scope{Type: findings.ScopeRoot, Path: root}) {
			t.Errorf("target %s scope = %+v", tg.Path, tg.Scope)
		}
	}
	if !slices.Equal(rec.allowed, []string{root}) {
		t.Errorf("guard allows %v, want exactly [%s]", rec.allowed, root)
	}
	if !strings.Contains(out, d.name) {
		t.Errorf("output = %q", out)
	}

	// The bare command takes the same path.
	rec.reset()
	if code, _, errOut := runScanCmd(t, root, "-d", d.name); code != ExitOK || len(rec.paths()) != 3 {
		t.Fatalf("bare command: code %d, targets %v, stderr %q", code, rec.paths(), errOut)
	}
}

// TestScanPathInsideARepoScansThatRepo: a path in a repository is the same
// as running from there, not a walk below the folder.
func TestScanPathInsideARepoScansThatRepo(t *testing.T) {
	needGit(t)
	isolate(t)
	repo := testutil.NewRepo(t)
	sub := filepath.Join(repo.Dir, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(testutil.ResolvedTempDir(t))
	d, rec := recordingFake(t, detect.CategoryFiles)
	if code, _, errOut := runScanCmd(t, "scan", sub, "-d", d.name); code != ExitOK {
		t.Fatalf("code %d, stderr %q", code, errOut)
	}
	root, _ := filepath.EvalSymlinks(repo.Dir)
	if got := rec.paths(); len(got) != 1 || got[0] != root {
		t.Errorf("targets = %v, want the repository %s", got, root)
	}
}

func TestScanPathErrors(t *testing.T) {
	isolate(t)
	d, rec := recordingFake(t, detect.CategoryFiles)
	missing := filepath.Join(testutil.ResolvedTempDir(t), "does-not-exist")
	fsRoot := "/"
	if runtime.GOOS == "windows" {
		fsRoot = `C:\`
	}
	dir := testutil.ResolvedTempDir(t)
	testutil.WriteFile(t, dir, "f", "x")
	file := filepath.Join(dir, "f")
	for _, tc := range []struct{ path, want string }{
		{missing, missing},
		{fsRoot, "filesystem root"},
		{file, "not a directory"},
	} {
		code, _, errOut := runScanCmd(t, "scan", tc.path, "-d", d.name)
		if code != ExitUsage || !strings.Contains(errOut, tc.want) {
			t.Errorf("%s: code %d, stderr %q", tc.path, code, errOut)
		}
	}
	if len(rec.paths()) != 0 {
		t.Errorf("a rejected path was scanned: %v", rec.paths())
	}
}

func TestScanPathThroughSymlink(t *testing.T) {
	isolate(t)
	root := testutil.ResolvedTempDir(t)
	fakeRepoDir(t, root, "a1")
	link := filepath.Join(testutil.ResolvedTempDir(t), "link")
	if err := os.Symlink(root, link); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	d, rec := recordingFake(t, detect.CategoryFiles)
	if code, _, errOut := runScanCmd(t, "scan", link, "-d", d.name); code != ExitOK {
		t.Fatalf("code %d, stderr %q", code, errOut)
	}
	if !slices.Equal(rec.allowed, []string{root}) || len(rec.paths()) != 1 {
		t.Errorf("allowed %v, targets %v; want the resolved folder", rec.allowed, rec.paths())
	}
}

// TestRemovedScopeFlags: --workspaces and --root are gone with the root
// registry.
func TestRemovedScopeFlags(t *testing.T) {
	isolate(t)
	for _, flag := range []string{"--workspaces", "-w", "--root=/x"} {
		code, _, errOut := runScanCmd(t, "scan", flag)
		if code != ExitUsage || !strings.Contains(errOut, "unknown") {
			t.Errorf("%s: code %d, stderr %q", flag, code, errOut)
		}
	}
}

func TestScanDetectorFlagValidation(t *testing.T) {
	isolate(t)
	d, _ := recordingFake(t, detect.CategoryFiles)
	code, _, errOut := runScanCmd(t, "scan", "-d", "nope")
	if code != ExitUsage {
		t.Fatalf("code = %d, stderr %q", code, errOut)
	}
	if !strings.Contains(errOut, `unknown detector "nope"`) || !strings.Contains(errOut, d.name) {
		t.Errorf("stderr %q must name the bad value and list valid names", errOut)
	}
}

func TestSelectDetectors(t *testing.T) {
	x := registerFake(t, detect.CategoryFiles, nil)
	y := registerFake(t, detect.CategoryFiles, nil)
	names := func(ds []detect.Detector) []string {
		out := make([]string, len(ds))
		for i, d := range ds {
			out[i] = d.Name()
		}
		return out
	}
	sorted := func(a, b string) []string {
		s := []string{a, b}
		sort.Strings(s)
		return s
	}
	tests := []struct {
		name       string
		flag, pre  []string
		want       []string
		wantUsage  bool
		wantInText []string
	}{
		{name: "flag only", flag: []string{x.name}, want: []string{x.name}},
		{name: "preset only", pre: []string{y.name}, want: []string{y.name}},
		{name: "duplicates collapse", flag: []string{x.name, x.name}, want: []string{x.name}},
		{name: "intersection", flag: []string{x.name, y.name}, pre: []string{y.name}, want: []string{y.name}},
		{name: "both equal", flag: []string{x.name, y.name}, pre: []string{x.name, y.name}, want: sorted(x.name, y.name)},
		{name: "empty intersection", flag: []string{x.name}, pre: []string{y.name}, wantUsage: true, wantInText: []string{x.name, y.name}},
		{name: "unknown preset", pre: []string{"missing"}, wantUsage: true, wantInText: []string{"missing"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := selectDetectors(tt.flag, tt.pre)
			if tt.wantUsage {
				var ue usageError
				if !errors.As(err, &ue) {
					t.Fatalf("err = %v, want usage error", err)
				}
				for _, s := range tt.wantInText {
					if !strings.Contains(err.Error(), s) {
						t.Errorf("error %q lacks %q", err, s)
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(names(got), tt.want) {
				t.Errorf("selected %v, want %v", names(got), tt.want)
			}
		})
	}
	all, err := selectDetectors(nil, nil)
	if err != nil || len(all) < 2 {
		t.Errorf("no selection must yield every registered detector, got %d (%v)", len(all), err)
	}
}

func TestPresetAndFlagIntersectionThroughScan(t *testing.T) {
	needGit(t)
	isolate(t)
	repo := testutil.NewRepo(t)
	t.Chdir(repo.Dir)
	x, _ := recordingFake(t, detect.CategoryGit)
	y, _ := recordingFake(t, detect.CategoryGit)
	a := &app{flags: globalFlags{detectors: []string{x.name}}}
	_, err := a.scan(context.Background(), scanOptions{detectors: []string{y.name}}, nil)
	var ue usageError
	if !errors.As(err, &ue) || !strings.Contains(err.Error(), x.name) || !strings.Contains(err.Error(), y.name) {
		t.Fatalf("err = %v, want usage error naming both", err)
	}
}

// requestWith builds a scan request through the normal validation and then
// swaps in detectors that are not registered, which lets a test use the
// real detector names the config toggles (registering those would collide
// with the real detectors).
func requestWith(t *testing.T, a *app, dets ...detect.Detector) *scanRequest {
	t.Helper()
	filler := registerFake(t, detect.CategoryFiles, nil)
	a.flags.detectors = []string{filler.name}
	req, err := a.newScanRequest(scanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	req.detectors = dets
	return req
}

// namesRun executes a workspace scan with the two unregistered detectors
// named like real ones and returns the sorted "detector@dir" pairs that ran.
func namesRun(t *testing.T, root string) []string {
	t.Helper()
	var mu sync.Mutex
	var ran []string
	fn := func(name string) detectFunc {
		return func(_ context.Context, _ *detect.Env, tg scope.Target, _ func(findings.Finding)) error {
			mu.Lock()
			defer mu.Unlock()
			ran = append(ran, name+"@"+filepath.Base(tg.Path))
			return nil
		}
	}
	build := &fakeDetector{name: config.DetectorBuildArtifacts, cat: detect.CategoryArtifacts, fn: fn("build")}
	logs := &fakeDetector{name: config.DetectorLogs, cat: detect.CategoryLogs, fn: fn("logs")}
	a := &app{io: IO{Out: &strings.Builder{}, Err: &strings.Builder{}}, flags: globalFlags{path: root}}
	req := requestWith(t, a, build, logs)
	if _, err := a.execute(context.Background(), req, nil); err != nil {
		t.Fatal(err)
	}
	sort.Strings(ran)
	return ran
}

func TestDetectorTogglesFromConfig(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, root string) map[string]any
		want  []string
	}{
		{"all enabled by default", func(t *testing.T, root string) map[string]any { return map[string]any{} },
			[]string{"build@one", "build@two", "logs@one", "logs@two"}},
		{"disabled globally", func(t *testing.T, root string) map[string]any {
			return map[string]any{"detectors": map[string]any{"build-artifacts": map[string]any{"enabled": false}}}
		}, []string{"logs@one", "logs@two"}},
		{"disabled by one repo's .brooom.json", func(t *testing.T, root string) map[string]any {
			testutil.WriteFile(t, filepath.Join(root, "two"), config.RepoConfigFileName, `{"disable":["build-artifacts"]}`)
			return map[string]any{}
		}, []string{"build@one", "logs@one", "logs@two"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := isolate(t)
			root := testutil.ResolvedTempDir(t)
			fakeRepoDir(t, root, "one")
			fakeRepoDir(t, root, "two")
			writeConfig(t, home, tt.setup(t, root))
			if got := namesRun(t, root); !slices.Equal(got, tt.want) {
				t.Errorf("ran %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAppliesFunc(t *testing.T) {
	on := config.Default()
	off := config.Default()
	off.Detectors.Worktrees.Enabled = false
	repo := scope.Target{Kind: scope.TargetRepo, Path: "/r"}
	proj := scope.Target{Kind: scope.TargetProject, Path: "/p"}
	user := scope.Target{Kind: scope.TargetUser, Path: "/u"}
	eff := map[string]*config.Config{"/r": on, "/p": on, "/u": on}
	effOff := map[string]*config.Config{"/r": off}

	gitDet := &fakeDetector{name: config.DetectorWorktrees, cat: detect.CategoryGit}
	fileDet := &fakeDetector{name: config.DetectorLogs, cat: detect.CategoryLogs}
	unknown := &fakeDetector{name: "not-in-config", cat: detect.CategoryFiles}
	tests := []struct {
		name string
		eff  map[string]*config.Config
		d    detect.Detector
		t    scope.Target
		want bool
	}{
		{"git on repo", eff, gitDet, repo, true},
		{"git on project", eff, gitDet, proj, false},
		{"git on user", eff, gitDet, user, false},
		{"files on project", eff, fileDet, proj, true},
		{"files on user", eff, fileDet, user, true},
		{"disabled", effOff, gitDet, repo, false},
		{"target without config", effOff, fileDet, proj, false},
		{"detector unknown to config stays enabled", eff, unknown, repo, true},
	}
	for _, tt := range tests {
		if got := appliesFunc(tt.eff)(tt.d, tt.t); got != tt.want {
			t.Errorf("%s: applies = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestGitDetectorSkippedOnProjectTarget(t *testing.T) {
	isolate(t)
	root := testutil.ResolvedTempDir(t)
	fakeRepoDir(t, root, "repo")
	projectDir(t, root, "proj")
	git, gitRec := recordingFake(t, detect.CategoryGit)
	files, filesRec := recordingFake(t, detect.CategoryFiles)

	code, _, errOut := runScanCmd(t, "scan", root, "-d", git.name+","+files.name)
	if code != ExitOK {
		t.Fatalf("code %d, stderr %q", code, errOut)
	}
	if got := gitRec.paths(); len(got) != 1 || got[0] != filepath.Join(root, "repo") {
		t.Errorf("git detector ran on %v, want only the repo", got)
	}
	if got := filesRec.paths(); len(got) != 2 {
		t.Errorf("files detector ran on %v, want repo and project", got)
	}
}

func TestBrokenRepoConfigSkipsOnlyThatTarget(t *testing.T) {
	isolate(t)
	root := testutil.ResolvedTempDir(t)
	good := fakeRepoDir(t, root, "good")
	bad := fakeRepoDir(t, root, "bad")
	testutil.WriteFile(t, bad, config.RepoConfigFileName, `{"disable":["no-such-detector"]}`)
	d, rec := recordingFake(t, detect.CategoryFiles)

	code, out, errOut := runScanCmd(t, "scan", root, "-d", d.name)
	if code != ExitOK {
		t.Fatalf("code %d, stderr %q", code, errOut)
	}
	if got := rec.paths(); len(got) != 1 || got[0] != good {
		t.Errorf("targets = %v, want only %s", got, good)
	}
	if !strings.Contains(out, bad) || !strings.Contains(out, "no-such-detector") {
		t.Errorf("the skipped target must be reported in the output:\n%s", out)
	}
}

func TestExtraTargetsFromTargetSource(t *testing.T) {
	needGit(t)
	isolate(t)
	repo := testutil.NewRepo(t)
	t.Chdir(repo.Dir)
	userDir := testutil.ResolvedTempDir(t)
	missingDir := filepath.Join(userDir, "missing")
	userScope := findings.Scope{Type: findings.ScopeUser, Path: userDir}

	var mu sync.Mutex
	var guardErr error
	var scanned []scope.Target
	src := &sourcedDetector{fakeDetector: newFake(detect.CategoryFiles, nil)}
	src.fn = func(_ context.Context, env *detect.Env, tg scope.Target, emit func(findings.Finding)) error {
		mu.Lock()
		scanned = append(scanned, tg)
		mu.Unlock()
		if tg.Kind == scope.TargetUser {
			if _, err := env.Guard.Resolve(filepath.Join(userDir, "file.log")); err != nil {
				mu.Lock()
				guardErr = err
				mu.Unlock()
			}
		}
		emitAt(src.fakeDetector, tg, emit, "")
		return nil
	}
	src.extra = func(context.Context, *config.Config) ([]scope.Target, error) {
		return []scope.Target{
			{Kind: scope.TargetUser, Path: userDir, Scope: userScope, Tool: "demo"},
			{Kind: scope.TargetUser, Path: userDir, Scope: userScope, Tool: "demo"},
			{Kind: scope.TargetUser, Path: missingDir},
			{Kind: scope.TargetRepo, Path: userDir},
		}, nil
	}
	detect.Register(src)
	plain, plainRec := recordingFake(t, detect.CategoryFiles)

	code, out, errOut := runScanCmd(t, "scan", "-d", src.name+","+plain.name)
	if code != ExitOK {
		t.Fatalf("code %d, stderr %q", code, errOut)
	}
	if src.calls.Load() != 1 {
		t.Errorf("ExtraTargets calls = %d, want 1", src.calls.Load())
	}
	users := 0
	for _, tg := range scanned {
		if tg.Kind == scope.TargetUser {
			users++
			if tg.Path != userDir || tg.Scope != userScope {
				t.Errorf("user target = %+v", tg)
			}
		}
	}
	if users != 1 {
		t.Errorf("user target scanned %d times, want once (deduplicated, missing dropped): %+v", users, scanned)
	}
	if guardErr != nil {
		t.Errorf("guard refused the user location: %v", guardErr)
	}
	if !slices.Contains(plainRec.allowed, userDir) {
		t.Errorf("guard allows %v, want the user location", plainRec.allowed)
	}
	if !strings.Contains(out, "only user targets are allowed") {
		t.Errorf("the rejected repo-kind extra target must be reported:\n%s", out)
	}
	if strings.Contains(out, missingDir) {
		t.Errorf("a missing user location must be dropped silently:\n%s", out)
	}
}

func TestTargetSourceErrorAndDisabledDetector(t *testing.T) {
	needGit(t)
	home := isolate(t)
	repo := testutil.NewRepo(t)
	t.Chdir(repo.Dir)
	writeConfig(t, home, map[string]any{"detectors": map[string]any{"ai-artifacts": map[string]any{"enabled": false}}})

	failing := &sourcedDetector{fakeDetector: newFake(detect.CategoryFiles, nil)}
	failing.extra = func(context.Context, *config.Config) ([]scope.Target, error) {
		return nil, errors.New("catalog unreadable")
	}
	disabled := &sourcedDetector{fakeDetector: &fakeDetector{name: config.DetectorAIArtifacts, cat: detect.CategoryAI}}
	disabled.extra = func(context.Context, *config.Config) ([]scope.Target, error) {
		t.Error("a globally disabled detector must not be asked for targets")
		return nil, nil
	}
	notSource := newFake(detect.CategoryFiles, nil)

	a := &app{io: IO{Out: &strings.Builder{}, Err: &strings.Builder{}}}
	req := requestWith(t, a, failing, disabled, notSource)
	res, err := a.execute(context.Background(), req, nil)
	if err != nil {
		t.Fatal(err)
	}
	if failing.calls.Load() != 1 || disabled.calls.Load() != 0 {
		t.Errorf("calls: failing=%d disabled=%d", failing.calls.Load(), disabled.calls.Load())
	}
	found := false
	for _, e := range res.Report.Errors {
		found = found || (e.Detector == failing.name && strings.Contains(e.Message, "catalog unreadable"))
	}
	if !found || len(res.Targets) != 1 {
		t.Errorf("errors = %+v, targets = %+v", res.Report.Errors, res.Targets)
	}
}

func TestEnvCacheDir(t *testing.T) {
	needGit(t)
	for _, enabled := range []bool{true, false} {
		t.Run(map[bool]string{true: "cache on", false: "cache off"}[enabled], func(t *testing.T) {
			home := isolate(t)
			repo := testutil.NewRepo(t)
			t.Chdir(repo.Dir)
			writeConfig(t, home, map[string]any{"scan": map[string]any{"cache": enabled}})
			d, rec := recordingFake(t, detect.CategoryFiles)
			if code, _, errOut := runScanCmd(t, "scan", "-d", d.name); code != ExitOK {
				t.Fatalf("code %d, stderr %q", code, errOut)
			}
			want := ""
			if enabled {
				want = filepath.Join(home, "cache")
			}
			if got := rec.envs[0].CacheDir; got != want {
				t.Errorf("CacheDir = %q, want %q", got, want)
			}
			if _, err := os.Stat(filepath.Join(home, "cache")); err == nil {
				t.Error("scanning must not create the cache directory itself")
			}
		})
	}
}

func TestEnvFields(t *testing.T) {
	needGit(t)
	isolate(t)
	repo := testutil.NewRepo(t)
	t.Chdir(repo.Dir)
	d, rec := recordingFake(t, detect.CategoryFiles)
	a := &app{io: IO{Out: &strings.Builder{}, Err: &strings.Builder{}}, flags: globalFlags{detectors: []string{d.name}}}
	res, err := a.scan(context.Background(), scanOptions{force: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	env := rec.envs[0]
	if !env.Force || env.Git == nil || env.Guard == nil || env.Now.IsZero() || env.Config == nil {
		t.Errorf("env = %+v", env)
	}
	if res.Env != env || res.Guard != env.Guard || res.Config != env.Config || len(res.Targets) != 1 {
		t.Errorf("scanResult must expose the env, guard, config and targets: %+v", res)
	}
	if !res.Report.GeneratedAt.Equal(env.Now.UTC()) {
		t.Errorf("report time %v != env.Now %v", res.Report.GeneratedAt, env.Now)
	}
}

// TestDetectorErrorIsReportedWithDistinctExit pins #191: a detector that
// failed is reported in the report and no longer looks like a clean scan.
func TestDetectorErrorIsReportedWithDistinctExit(t *testing.T) {
	needGit(t)
	isolate(t)
	repo := testutil.NewRepo(t)
	t.Chdir(repo.Dir)
	d := registerFake(t, detect.CategoryFiles, func(context.Context, *detect.Env, scope.Target, func(findings.Finding)) error {
		return errors.New("kaboom while scanning")
	})
	code, out, _ := runScanCmd(t, "scan", "-d", d.name)
	if code != ExitDetectorFailed {
		t.Fatalf("code = %d, want %d for a failed detector", code, ExitDetectorFailed)
	}
	if !strings.Contains(out, "kaboom while scanning") {
		t.Errorf("error not rendered:\n%s", out)
	}
}

func TestPathOutsideScopeIsRefusedByGuard(t *testing.T) {
	needGit(t)
	isolate(t)
	repo := testutil.NewRepo(t)
	t.Chdir(repo.Dir)
	outside := testutil.ResolvedTempDir(t)
	testutil.WriteFile(t, outside, "secret.txt", "x")

	var refused error
	var d *fakeDetector
	d = registerFake(t, detect.CategoryFiles, func(_ context.Context, env *detect.Env, tg scope.Target, emit func(findings.Finding)) error {
		p, err := env.Guard.Resolve(filepath.Join(outside, "secret.txt"))
		refused = err
		if err == nil {
			emit(findings.Finding{ID: "leak", Detector: d.name, Scope: tg.Scope, Path: p, Kind: findings.KindFile})
		}
		return nil
	})
	code, out, _ := runScanCmd(t, "scan", "-d", d.name)
	if code != ExitOK || !errors.Is(refused, scope.ErrOutsideScope) {
		t.Fatalf("code %d, refused = %v", code, refused)
	}
	if strings.Contains(out, "secret.txt") {
		t.Errorf("outside path leaked into the report:\n%s", out)
	}
}

func TestNoColor(t *testing.T) {
	needGit(t)
	tests := []struct {
		name     string
		args     []string
		env      string
		cfgColor string
		wantANSI bool
	}{
		{name: "always in config", cfgColor: "always", wantANSI: true},
		{name: "flag beats config always", args: []string{"--no-color"}, cfgColor: "always"},
		{name: "config never", cfgColor: "never"},
		{name: "NO_COLOR with auto", env: "1", cfgColor: "auto"},
		{name: "config always beats NO_COLOR", env: "1", cfgColor: "always", wantANSI: true},
		{name: "piped default", cfgColor: "auto"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := isolate(t)
			t.Setenv("NO_COLOR", tt.env)
			repo := testutil.NewRepo(t)
			t.Chdir(repo.Dir)
			writeConfig(t, home, map[string]any{"output": map[string]any{"color": tt.cfgColor}})
			d, _ := recordingFake(t, detect.CategoryFiles)
			args := append([]string{"scan", "-d", d.name}, tt.args...)
			code, out, _ := runScanCmd(t, args...)
			if code != ExitOK {
				t.Fatalf("code %d", code)
			}
			if got := strings.Contains(out, "\x1b"); got != tt.wantANSI {
				t.Errorf("ANSI present = %v, want %v\n%q", got, tt.wantANSI, out)
			}
		})
	}
}

func TestQuietAndVerbose(t *testing.T) {
	needGit(t)
	isolate(t)
	repo := testutil.NewRepo(t)
	t.Chdir(repo.Dir)
	d, _ := recordingFake(t, detect.CategoryFiles)
	failing := registerFake(t, detect.CategoryFiles, func(context.Context, *detect.Env, scope.Target, func(findings.Finding)) error {
		// A note keeps the exit code at 0 so the test isolates verbosity.
		return detect.Note(errors.New("verbose-visible failure"))
	})
	both := d.name + "," + failing.name

	code, out, errOut := runScanCmd(t, "scan", "-d", both)
	if code != ExitOK || errOut != "" {
		t.Errorf("default run must be silent on stderr: code %d, stderr %q", code, errOut)
	}
	if !strings.Contains(out, "verbose-visible failure") {
		t.Errorf("stdout must carry the scan error rendering:\n%s", out)
	}

	code, out, errOut = runScanCmd(t, "scan", "-v", "-d", both)
	if code != ExitOK {
		t.Fatalf("code %d", code)
	}
	for _, want := range []string{"config:", "1 target(s)", "2 detector(s)", d.name + ": 1 finding(s)", failing.name + ": 0 finding(s)", "verbose-visible failure"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("verbose stderr lacks %q:\n%s", want, errOut)
		}
	}
	if strings.Contains(out, "config:") || strings.Contains(out, "target(s)") {
		t.Errorf("progress must never reach stdout:\n%s", out)
	}

	code, _, errOut = runScanCmd(t, "scan", "-q", "-v", "-d", both)
	if code != ExitOK || errOut != "" {
		t.Errorf("--quiet must suppress all stderr progress even with --verbose: code %d, %q", code, errOut)
	}
}
