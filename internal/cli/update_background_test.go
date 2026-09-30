package cli

import (
	"bytes"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/updatecheck"
)

const noticeLine = "brooom 2.0.0 is available, run 'brooom update-check'"

func cachePath(t *testing.T) string {
	t.Helper()
	home, err := config.Home()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(home, "cache", "update.json")
}

func TestBackgroundNoticeOnStderrOnly(t *testing.T) {
	f := newReleaseFixture(t, 200, "v2.0.0", 0)
	a, out, errOut := newTestApp(t, "1.0.0")
	enableBackground(a)
	a.update.grace = 5 * time.Second // the fixture answers instantly; avoid flakes on slow CI
	if code := execute(a, []string{"version"}); code != ExitOK {
		t.Fatalf("code = %d", code)
	}
	if strings.TrimSpace(errOut.String()) != noticeLine {
		t.Errorf("stderr = %q, want %q", errOut, noticeLine)
	}
	if strings.Contains(out.String(), "available") || !strings.HasPrefix(out.String(), "brooom ") {
		t.Errorf("stdout polluted: %q", out)
	}
	if f.hits.Load() != 1 {
		t.Errorf("hits = %d, want 1", f.hits.Load())
	}
	cached, err := updatecheck.ReadCache(cachePath(t))
	if err != nil || cached.Latest != "v2.0.0" {
		t.Errorf("cache = %+v, %v", cached, err)
	}
}

func TestBackgroundUsesFreshCache(t *testing.T) {
	f := newReleaseFixture(t, 200, "v9.0.0", 0)
	a, _, errOut := newTestApp(t, "1.0.0")
	enableBackground(a)
	now := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	a.update.now = func() time.Time { return now }
	a.update.grace = 5 * time.Second
	if err := updatecheck.WriteCache(cachePath(t), updatecheck.Cache{CheckedAt: now.Add(-time.Hour), Latest: "v2.0.0"}); err != nil {
		t.Fatal(err)
	}
	execute(a, []string{"version"})
	if f.hits.Load() != 0 || strings.TrimSpace(errOut.String()) != noticeLine {
		t.Errorf("hits %d stderr %q", f.hits.Load(), errOut)
	}
}

func TestBackgroundExpiredCacheRefetches(t *testing.T) {
	f := newReleaseFixture(t, 200, "v3.0.0", 0)
	a, _, errOut := newTestApp(t, "1.0.0")
	enableBackground(a)
	now := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	a.update.now = func() time.Time { return now }
	a.update.grace = 5 * time.Second
	if err := updatecheck.WriteCache(cachePath(t), updatecheck.Cache{CheckedAt: now.Add(-25 * time.Hour), Latest: "v1.0.0"}); err != nil {
		t.Fatal(err)
	}
	execute(a, []string{"version"})
	if f.hits.Load() != 1 || !strings.Contains(errOut.String(), "brooom 3.0.0 is available") {
		t.Errorf("hits %d stderr %q", f.hits.Load(), errOut)
	}
}

func TestBackgroundNoNoticeWhenCurrent(t *testing.T) {
	for _, current := range []string{"2.0.0", "3.0.0"} {
		newReleaseFixture(t, 200, "v2.0.0", 0)
		a, _, errOut := newTestApp(t, current)
		enableBackground(a)
		a.update.grace = 5 * time.Second
		execute(a, []string{"version"})
		if errOut.Len() != 0 {
			t.Errorf("current %s: stderr = %q, want empty", current, errOut)
		}
	}
}

// TestBackgroundNeverFailsCommand covers a dead server, a rate limit and an
// invalid answer: the command still succeeds and prints nothing extra.
func TestBackgroundNeverFailsCommand(t *testing.T) {
	for _, status := range []int{500, 403, 429} {
		f := newReleaseFixture(t, status, "", 0)
		a, out, errOut := newTestApp(t, "1.0.0")
		enableBackground(a)
		a.update.grace = 5 * time.Second
		if code := execute(a, []string{"version"}); code != ExitOK {
			t.Errorf("status %d: code = %d", status, code)
		}
		if errOut.Len() != 0 || !strings.HasPrefix(out.String(), "brooom ") {
			t.Errorf("status %d: stdout %q stderr %q", status, out, errOut)
		}
		if f.hits.Load() != 1 {
			t.Errorf("status %d: hits %d", status, f.hits.Load())
		}
	}
	f := newReleaseFixture(t, 200, "v2.0.0", 0)
	f.srv.Close()
	a, _, errOut := newTestApp(t, "1.0.0")
	enableBackground(a)
	a.update.grace = 5 * time.Second
	if code := execute(a, []string{"version"}); code != ExitOK || errOut.Len() != 0 {
		t.Errorf("server down: code %d stderr %q", code, errOut)
	}
}

func TestBackgroundGraceDoesNotBlock(t *testing.T) {
	newReleaseFixture(t, 200, "v2.0.0", 10*time.Second)
	a, _, errOut := newTestApp(t, "1.0.0")
	enableBackground(a)
	start := time.Now()
	if code := execute(a, []string{"version"}); code != ExitOK {
		t.Fatalf("code = %d", code)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("command waited %v for the background check", elapsed)
	}
	if errOut.Len() != 0 {
		t.Errorf("stderr = %q, want no notice after grace expiry", errOut)
	}
}

// A request slower than the grace is cancelled at exit, but the attempt is
// recorded, so the next command must not start another request.
func TestBackgroundSlowRequestIsNotRetriedImmediately(t *testing.T) {
	f := newReleaseFixture(t, 200, "v2.0.0", 10*time.Second)
	a, _, _ := newTestApp(t, "1.0.0")
	enableBackground(a)
	a.update.grace = 100 * time.Millisecond
	if code := execute(a, []string{"version"}); code != ExitOK {
		t.Fatalf("code = %d", code)
	}
	// The request may still be on its way to the server when the command ends.
	deadline := time.Now().Add(2 * time.Second)
	for f.hits.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if f.hits.Load() != 1 {
		t.Fatalf("hits after first run = %d, want 1", f.hits.Load())
	}
	// A second invocation shares the Brooom home (and so the cache file).
	var errOut bytes.Buffer
	b := &app{io: IO{In: strings.NewReader(""), Out: &bytes.Buffer{}, Err: &errOut}}
	b.update = a.update
	if code := execute(b, []string{"version"}); code != ExitOK {
		t.Fatalf("code = %d", code)
	}
	time.Sleep(200 * time.Millisecond)
	if f.hits.Load() != 1 {
		t.Errorf("hits after second run = %d, want 1 (attempt must be recorded)", f.hits.Load())
	}
	if errOut.Len() != 0 {
		t.Errorf("stderr = %q", errOut)
	}
}

func TestBackgroundSkipConditions(t *testing.T) {
	tests := []struct {
		name  string
		args  []string
		setup func(t *testing.T, a *app)
	}{
		{"config disabled", []string{"version"}, func(t *testing.T, a *app) {
			a.update.loadConfig = func(string) (*config.Config, error) { return &config.Config{}, nil }
		}},
		{"config broken", []string{"version"}, func(t *testing.T, a *app) {
			a.update.loadConfig = func(string) (*config.Config, error) { return nil, errors.New("bad config") }
		}},
		{"env opt-out", []string{"version"}, func(t *testing.T) func(*testing.T, *app) {
			return func(t *testing.T, a *app) { t.Setenv(NoUpdateCheckEnv, "1") }
		}(t)},
		{"not a terminal", []string{"version"}, func(t *testing.T, a *app) {
			a.update.stdoutTTY = func() bool { return false }
		}},
		{"quiet", []string{"version", "--quiet"}, nil},
		{"json format", []string{"version", "--format", "json"}, nil},
		{"plain format", []string{"version", "--format", "plain"}, nil},
		{"dev build", []string{"version"}, func(t *testing.T, a *app) {
			a.update.version = func() string { return "dev" }
		}},
		{"completion", []string{"completion", "bash"}, nil},
		{"complete", []string{"__complete", "ver"}, nil},
		{"update-check itself", []string{"update-check"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newReleaseFixture(t, 200, "v2.0.0", 0)
			a, out, errOut := newTestApp(t, "1.0.0")
			enableBackground(a)
			a.update.grace = 5 * time.Second
			if tt.setup != nil {
				tt.setup(t, a)
			}
			execute(a, tt.args)
			if tt.args[0] != "update-check" && f.hits.Load() != 0 {
				t.Errorf("network was contacted %d times", f.hits.Load())
			}
			if strings.Contains(errOut.String(), "is available, run") || strings.Contains(out.String(), "is available, run") {
				t.Errorf("notice printed: stdout %q stderr %q", out, errOut)
			}
		})
	}
}

// TestBackgroundDefaultConfigLoadIsSilent uses the real config.Load, which
// may be unimplemented or fail: that must simply mean "no check".
func TestBackgroundDefaultConfigLoadIsSilent(t *testing.T) {
	f := newReleaseFixture(t, 200, "v2.0.0", 0)
	a, _, errOut := newTestApp(t, "1.0.0")
	a.update.stdoutTTY = func() bool { return true }
	if code := execute(a, []string{"version"}); code != ExitOK {
		t.Fatalf("code = %d", code)
	}
	if f.hits.Load() != 0 || strings.Contains(errOut.String(), "not implemented") {
		t.Errorf("hits %d stderr %q", f.hits.Load(), errOut)
	}
}

func TestStdoutIsTerminalWithBuffer(t *testing.T) {
	a, _, _ := newTestApp(t, "1.0.0")
	if a.stdoutIsTerminal() {
		t.Error("a bytes.Buffer is not a terminal")
	}
}

// TestPostRunHooksCoexist proves the update notice does not displace other
// post-run hooks (such as the retention notice of #26): every registered hook
// runs, in registration order, in a single invocation.
func TestPostRunHooksCoexist(t *testing.T) {
	newReleaseFixture(t, 200, "v2.0.0", 0)
	a, _, errOut := newTestApp(t, "1.0.0")
	enableBackground(a)
	a.update.grace = 5 * time.Second
	a.postRunHooks = append(a.postRunHooks, func(cmd *cobra.Command, args []string) {
		_, _ = a.io.Err.Write([]byte("retention notice\n"))
	})
	// newRootCmd appends the update hook after the pre-registered one.
	if code := execute(a, []string{"version"}); code != ExitOK {
		t.Fatalf("code = %d", code)
	}
	lines := strings.Split(strings.TrimSpace(errOut.String()), "\n")
	if len(lines) != 2 || lines[0] != "retention notice" || lines[1] != noticeLine {
		t.Errorf("stderr lines = %q, want retention notice then update notice", lines)
	}
}

func TestPostRunHooksRunInOrder(t *testing.T) {
	a, _, _ := newTestApp(t, "dev")
	var order []int
	for i := 1; i <= 3; i++ {
		a.postRunHooks = append(a.postRunHooks, func(*cobra.Command, []string) { order = append(order, i) })
	}
	if execute(a, []string{"version"}) != ExitOK {
		t.Fatal("version failed")
	}
	if len(order) != 3 || order[0] != 1 || order[1] != 2 || order[2] != 3 {
		t.Errorf("order = %v", order)
	}
}

func TestRootDoesNotAssignPostRunOnSubcommands(t *testing.T) {
	a := &app{}
	root := newRootCmd(a)
	if root.PersistentPostRun == nil || len(a.postRunHooks) == 0 {
		t.Fatal("root must run the hook list")
	}
	for _, c := range root.Commands() {
		if c.PersistentPostRun != nil || c.PersistentPostRunE != nil || c.PersistentPreRun != nil || c.PersistentPreRunE != nil {
			t.Errorf("command %s defines a persistent hook that would shadow the root's", c.Name())
		}
	}
}
