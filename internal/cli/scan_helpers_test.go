package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/detect"
	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/scope"
	"github.com/Tobias-Braun/brooom/internal/testutil"
)

// fakeCounter makes registered detector and formatter names unique across
// the whole test binary: the registries are global and reject duplicates.
var fakeCounter atomic.Int64

// detectFunc is the body of a fake detector.
type detectFunc func(ctx context.Context, env *detect.Env, t scope.Target, emit func(findings.Finding)) error

// fakeDetector is a detector whose behaviour a test supplies.
type fakeDetector struct {
	name string
	cat  detect.Category
	fn   detectFunc
	// retired makes the detector a no-op once its test ended. The registry is
	// global and cannot unregister, and a scan without -d runs every
	// registered detector, so a fake that fails would otherwise turn every
	// later scan of the test binary into a failed one.
	retired atomic.Bool
}

func (d *fakeDetector) Name() string              { return d.name }
func (d *fakeDetector) Description() string       { return "fake detector " + d.name }
func (d *fakeDetector) Category() detect.Category { return d.cat }
func (d *fakeDetector) Detect(ctx context.Context, env *detect.Env, t scope.Target, emit func(findings.Finding)) error {
	if d.fn == nil || d.retired.Load() {
		return nil
	}
	return d.fn(ctx, env, t, emit)
}

// sourcedDetector is a fakeDetector that also implements detect.TargetSource.
type sourcedDetector struct {
	*fakeDetector
	extra func(ctx context.Context, cfg *config.Config) ([]scope.Target, error)
	calls atomic.Int64
}

func (d *sourcedDetector) ExtraTargets(ctx context.Context, cfg *config.Config) ([]scope.Target, error) {
	d.calls.Add(1)
	return d.extra(ctx, cfg)
}

// newFake creates a detector with a unique name without registering it.
func newFake(cat detect.Category, fn detectFunc) *fakeDetector {
	return &fakeDetector{name: fmt.Sprintf("fake-%d", fakeCounter.Add(1)), cat: cat, fn: fn}
}

// registerFake registers a fake detector under a unique name.
func registerFake(t *testing.T, cat detect.Category, fn detectFunc) *fakeDetector {
	t.Helper()
	d := newFake(cat, fn)
	detect.Register(d)
	t.Cleanup(func() { d.retired.Store(true) })
	return d
}

// emitAt emits one finding for the target path, so tests can see which
// targets were scanned in the report.
func emitAt(d *fakeDetector, t scope.Target, emit func(findings.Finding), ref string) {
	emit(findings.Finding{
		ID:              findings.NewID(d.name, findings.KindDir, t.Path, ref),
		Detector:        d.name,
		Scope:           t.Scope,
		Path:            t.Path,
		Kind:            findings.KindDir,
		Ref:             ref,
		Confidence:      findings.ConfidenceHigh,
		SuggestedAction: findings.SuggestedAction{Type: findings.ActionTrash},
	})
}

// recorder collects what fake detectors observed, safe for the parallel
// engine.
type recorder struct {
	mu      sync.Mutex
	targets []scope.Target
	allowed []string
	envs    []*detect.Env
}

func (r *recorder) record(env *detect.Env, t scope.Target) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.targets = append(r.targets, t)
	r.envs = append(r.envs, env)
	r.allowed = env.Guard.Allowed()
}

func (r *recorder) paths() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.targets))
	for i, t := range r.targets {
		out[i] = t.Path
	}
	return out
}

// recordingFake registers a fake that records targets and emits one finding
// per target.
func recordingFake(t *testing.T, cat detect.Category) (*fakeDetector, *recorder) {
	t.Helper()
	rec := &recorder{}
	var d *fakeDetector
	d = registerFake(t, cat, func(_ context.Context, env *detect.Env, tg scope.Target, emit func(findings.Finding)) error {
		rec.record(env, tg)
		emitAt(d, tg, emit, "")
		return nil
	})
	return d, rec
}

// isolate points BROOOM_HOME, HOME, USERPROFILE and XDG_DATA_HOME at temp
// dirs so no test touches the real home or trash, and returns the Brooom
// home.
func isolate(t *testing.T) string {
	t.Helper()
	home := testutil.ResolvedTempDir(t)
	fake := testutil.ResolvedTempDir(t)
	t.Setenv(config.HomeEnv, home)
	t.Setenv("HOME", fake)
	t.Setenv("USERPROFILE", fake)
	t.Setenv("XDG_DATA_HOME", filepath.Join(fake, "share"))
	t.Setenv("NO_COLOR", "")
	return home
}

// writeConfig writes the global config file below the Brooom home.
func writeConfig(t *testing.T, home string, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, config.ConfigFileName)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// rootsConfig returns a config value with the given roots.
func rootsConfig(paths ...string) map[string]any {
	roots := make([]map[string]any, len(paths))
	for i, p := range paths {
		roots[i] = map[string]any{"path": p}
	}
	return map[string]any{"roots": roots}
}

// runCtx executes the CLI in-process and returns code, stdout and stderr.
func runCtx(t *testing.T, ctx context.Context, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := executeContext(ctx, &app{io: IO{In: strings.NewReader(""), Out: &out, Err: &errOut}}, args)
	return code, out.String(), errOut.String()
}

// runScanCmd runs `brooom <args>` with a background context.
func runScanCmd(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	return runCtx(t, context.Background(), args...)
}

// fakeRepoDir creates a directory that discovery recognizes as a repository
// without invoking git (a .git directory is enough for scope.Discover).
func fakeRepoDir(t *testing.T, parent, name string) string {
	t.Helper()
	dir := filepath.Join(parent, name)
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// projectDir creates a non-git project folder (has a go.mod marker).
func projectDir(t *testing.T, parent, name string) string {
	t.Helper()
	dir := filepath.Join(parent, name)
	testutil.WriteFile(t, dir, "go.mod", "module x\n")
	return dir
}
