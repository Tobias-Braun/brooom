package cli

import (
	"context"
	"sync"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/detect"
	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/scope"
	"github.com/Tobias-Braun/brooom/internal/testutil"
)

// TestExtraTargetsKeptPerToolAndDetector pins the dedup key (detector, tool,
// resolved path): two tools sharing a base directory must each be scanned,
// only an exact repeat is dropped. Keyed by path alone, the second tool's
// locations were never reported and results depended on catalog order.
func TestExtraTargetsKeptPerToolAndDetector(t *testing.T) {
	needGit(t)
	isolate(t)
	repo := testutil.NewRepo(t)
	t.Chdir(repo.Dir)
	base := testutil.ResolvedTempDir(t)
	sc := findings.Scope{Type: findings.ScopeUser, Path: base}

	var mu sync.Mutex
	seen := map[string]int{}
	d := &sourcedDetector{fakeDetector: newFake(detect.CategoryFiles, nil)}
	d.fn = func(_ context.Context, _ *detect.Env, tg scope.Target, _ func(findings.Finding)) error {
		if tg.Kind == scope.TargetUser {
			mu.Lock()
			seen[tg.Tool]++
			mu.Unlock()
		}
		return nil
	}
	d.extra = func(context.Context, *config.Config) ([]scope.Target, error) {
		var out []scope.Target
		for _, tool := range []string{"claude-code", "custom-logs", "custom-logs"} {
			out = append(out, scope.Target{Kind: scope.TargetUser, Path: base, Scope: sc, Tool: tool})
		}
		return out, nil
	}
	detect.Register(d)

	if code, _, errOut := runScanCmd(t, "scan", "-d", d.name); code != ExitOK {
		t.Fatalf("code %d, stderr %q", code, errOut)
	}
	if seen["claude-code"] != 1 || seen["custom-logs"] != 1 {
		t.Errorf("each tool of a shared base must be scanned exactly once: %v", seen)
	}
}

// TestUserTargetKeyIncludesDetector checks the key directly: the same tool
// and path declared by two detectors are two targets.
func TestUserTargetKeyIncludesDetector(t *testing.T) {
	base := testutil.ResolvedTempDir(t)
	a := userTargetKey("a", scope.Target{Path: base, Tool: "x"})
	b := userTargetKey("b", scope.Target{Path: base, Tool: "x"})
	c := userTargetKey("a", scope.Target{Path: base, Tool: "y"})
	if a == b || a == c || b == c {
		t.Errorf("keys must differ per detector and tool: %v %v %v", a, b, c)
	}
	if a != userTargetKey("a", scope.Target{Path: base, Tool: "x"}) {
		t.Error("identical inputs must yield the same key")
	}
}
