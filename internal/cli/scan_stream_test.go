package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/detect"
	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/output"
	"github.com/Tobias-Braun/brooom/internal/scope"
	"github.com/Tobias-Braun/brooom/internal/testutil"
)

// streamFormatter is a formatter registered by a test that implements the
// CLI-local streamingFormatter interface.
type streamFormatter struct {
	name string

	mu       sync.Mutex
	got      []string
	wrote    bool
	finished bool
	out      io.Writer
}

func (f *streamFormatter) Name() string { return f.name }

func (f *streamFormatter) Write(io.Writer, *findings.Report, output.Options) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.wrote = true
	return nil
}

func (f *streamFormatter) NewStream(w io.Writer, _ output.Options) (func(findings.Finding), func() error) {
	f.out = w
	onFinding := func(fd findings.Finding) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.got = append(f.got, fd.Ref)
		fmt.Fprintln(w, "streamed", fd.Ref)
	}
	finish := func() error {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.finished = true
		return nil
	}
	return onFinding, finish
}

func TestStreamingFormatterReceivesFindingsInOrder(t *testing.T) {
	needGit(t)
	isolate(t)
	repo := testutil.NewRepo(t)
	t.Chdir(repo.Dir)
	sf := &streamFormatter{name: fmt.Sprintf("stream-%d", fakeCounter.Add(1))}
	output.Register(sf)

	var d *fakeDetector
	d = registerFake(t, detect.CategoryFiles, func(_ context.Context, _ *detect.Env, tg scope.Target, emit func(findings.Finding)) error {
		for _, ref := range []string{"one", "two", "three"} {
			emitAt(d, tg, emit, ref)
		}
		return errors.New("detector failed midway")
	})

	code, out, errOut := runScanCmd(t, "scan", "-d", d.name, "-f", sf.name)
	if code != ExitDetectorFailed {
		t.Fatalf("code %d, stderr %q", code, errOut)
	}
	if !slices.Equal(sf.got, []string{"one", "two", "three"}) {
		t.Errorf("OnFinding order = %v", sf.got)
	}
	if sf.wrote || !sf.finished {
		t.Errorf("Write called = %v (must not), finish called = %v (must)", sf.wrote, sf.finished)
	}
	if !strings.Contains(out, "streamed one") {
		t.Errorf("stream output = %q", out)
	}
	if !strings.Contains(errOut, "detector failed midway") {
		t.Errorf("scan errors must go to stderr in streaming mode: %q", errOut)
	}
}

func TestUnknownFormatIsUsageError(t *testing.T) {
	needGit(t)
	isolate(t)
	repo := testutil.NewRepo(t)
	t.Chdir(repo.Dir)
	d, rec := recordingFake(t, detect.CategoryFiles)

	code, _, errOut := runScanCmd(t, "scan", "-d", d.name, "-f", "bogus")
	if code != ExitUsage {
		t.Fatalf("flag: code %d, stderr %q", code, errOut)
	}
	for _, name := range output.Names() {
		if !strings.Contains(errOut, name) {
			t.Errorf("stderr %q does not list format %q", errOut, name)
		}
	}
	if len(rec.paths()) != 0 {
		t.Error("a format typo must fail before the scan starts")
	}

	// resolveFormat covers the config source; config validation itself
	// rejects unknown values earlier, so exercise the function directly.
	if _, err := resolveFormat("", "bogus"); err == nil {
		t.Error("unknown config format accepted")
	}
	if got, err := resolveFormat("", ""); err != nil || got != "table" {
		t.Errorf("default format = %q, %v", got, err)
	}
	if got, err := resolveFormat("summary", "table"); err != nil || got != "summary" {
		t.Errorf("flag must win: %q, %v", got, err)
	}
}

func TestConfigOutputFormatIsUsed(t *testing.T) {
	needGit(t)
	home := isolate(t)
	repo := testutil.NewRepo(t)
	t.Chdir(repo.Dir)
	d, _ := recordingFake(t, detect.CategoryFiles)
	// The config validator only accepts built-in format names, so a
	// built-in one with a recognizable output is used.
	writeConfig(t, home, map[string]any{"output": map[string]any{"format": "plain"}})
	code, out, _ := runScanCmd(t, "scan", "-d", d.name)
	if code != ExitOK || !strings.Contains(out, repo.Dir) {
		t.Errorf("plain format from config must print the path: code %d, out %q", code, out)
	}
}

func TestExplicitConfigFile(t *testing.T) {
	needGit(t)
	isolate(t)
	repo := testutil.NewRepo(t)
	t.Chdir(repo.Dir)
	d, _ := recordingFake(t, detect.CategoryFiles)

	missing := filepath.Join(t.TempDir(), "nope.json")
	code, _, errOut := runScanCmd(t, "scan", "--config", missing, "-d", d.name)
	if code != ExitError || !strings.Contains(errOut, "config file not found: "+missing) {
		t.Errorf("missing explicit config: code %d, stderr %q", code, errOut)
	}

	// Without --config a missing default file simply means defaults.
	if code, _, errOut := runScanCmd(t, "scan", "-d", d.name); code != ExitOK {
		t.Errorf("default config missing: code %d, stderr %q", code, errOut)
	}

	bad := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(bad, []byte(`{"scan":{"max_depht":3}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, errOut = runScanCmd(t, "scan", "--config", bad, "-d", d.name)
	if code != ExitError || !strings.Contains(errOut, "max_depht") {
		t.Errorf("invalid config must name the key: code %d, stderr %q", code, errOut)
	}
}

func TestInterruptedScanRendersPartialReport(t *testing.T) {
	needGit(t)
	isolate(t)
	repo := testutil.NewRepo(t)
	t.Chdir(repo.Dir)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var d *fakeDetector
	d = registerFake(t, detect.CategoryFiles, func(_ context.Context, _ *detect.Env, tg scope.Target, emit func(findings.Finding)) error {
		emitAt(d, tg, emit, "partial")
		cancel() // simulates Ctrl-C after the first finding
		return nil
	})
	code, out, errOut := runCtx(t, ctx, "scan", "-d", d.name)
	if code != ExitError {
		t.Fatalf("code = %d, want 1 (stderr %q)", code, errOut)
	}
	if !strings.Contains(errOut, "scan interrupted") {
		t.Errorf("stderr = %q", errOut)
	}
	if !strings.Contains(out, d.name) {
		t.Errorf("the partial report must still be rendered:\n%s", out)
	}
}

func TestAppScanReturnsPartialResultWhenInterrupted(t *testing.T) {
	needGit(t)
	isolate(t)
	repo := testutil.NewRepo(t)
	t.Chdir(repo.Dir)
	d, _ := recordingFake(t, detect.CategoryFiles)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	a := &app{io: IO{Out: io.Discard, Err: io.Discard}, flags: globalFlags{detectors: []string{d.name}}}
	res, err := a.scan(ctx, scanOptions{}, nil)
	if !errors.Is(err, errScanInterrupted) || res == nil || res.Guard == nil {
		t.Fatalf("res = %v, err = %v", res, err)
	}
}

func TestMissingGitOnlyFatalForGitDetectorOnRepo(t *testing.T) {
	gitErr := errors.New("no git")
	repo := []scope.Target{{Kind: scope.TargetRepo}}
	project := []scope.Target{{Kind: scope.TargetProject}}
	gitDet := &fakeDetector{name: "g", cat: detect.CategoryGit}
	fileDet := &fakeDetector{name: "f", cat: detect.CategoryFiles}
	tests := []struct {
		name    string
		err     error
		dets    []detect.Detector
		targets []scope.Target
		wantErr bool
	}{
		{"git present", nil, []detect.Detector{gitDet}, repo, false},
		{"git detector on repo", gitErr, []detect.Detector{gitDet}, repo, true},
		{"git detector on project only", gitErr, []detect.Detector{gitDet}, project, false},
		{"no git detector", gitErr, []detect.Detector{fileDet}, repo, false},
	}
	for _, tt := range tests {
		err := requireGit(tt.err, tt.dets, tt.targets)
		if (err != nil) != tt.wantErr {
			t.Errorf("%s: err = %v, wantErr %v", tt.name, err, tt.wantErr)
		}
		if err != nil && !errors.Is(err, gitErr) {
			t.Errorf("%s: %v does not wrap the cause", tt.name, err)
		}
	}
}

func TestMergeTargetsPrefersInnermostRootAndSorts(t *testing.T) {
	outer := findings.Scope{Type: findings.ScopeRoot, Path: "/w"}
	inner := findings.Scope{Type: findings.ScopeRoot, Path: "/w/sub"}
	in := []scope.Target{
		{Kind: scope.TargetRepo, Path: "/w/sub/b", Scope: outer},
		{Kind: scope.TargetRepo, Path: "/w/a", Scope: outer},
		{Kind: scope.TargetRepo, Path: "/w/sub/b", Scope: inner},
	}
	got := mergeTargets(in)
	if len(got) != 2 || got[0].Path != "/w/a" || got[1].Path != "/w/sub/b" || got[1].Scope != inner {
		t.Errorf("mergeTargets = %+v", got)
	}
	if s := scopesOf(got); len(s) != 2 || s[0] != outer || s[1] != inner {
		t.Errorf("scopesOf = %+v", s)
	}
}
