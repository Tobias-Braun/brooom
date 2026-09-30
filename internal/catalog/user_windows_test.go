//go:build windows

package catalog

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestUserProtectionThroughJunction covers a junction below the user
// directories (for example ~\.tool pointing elsewhere): paths under the
// junction target and paths through the junction are protected, an
// unprotected sibling under the target is not.
func TestUserProtectionThroughJunction(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	target := filepath.Join(root, "elsewhere", "tool")
	for _, d := range []string{home, filepath.Join(target, "skills"), filepath.Join(target, "todos")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	junction := filepath.Join(home, ".tool")
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", junction, target).CombinedOutput(); err != nil {
		t.Skipf("cannot create a junction: %v: %s", err, out)
	}
	t.Cleanup(func() { _ = os.Remove(junction) })

	p := userCatalog(t, "windows").UserProtection(PathEnv{GOOS: "windows", Home: home})
	if !p.Protected(filepath.Join(target, "skills", "a.md")) {
		t.Error("path under the junction target must be protected")
	}
	if !p.Protected(filepath.Join(junction, "skills", "a.md")) {
		t.Error("path through the junction must be protected")
	}
	if p.Protected(filepath.Join(target, "todos", "a.json")) {
		t.Error("unprotected sibling under the junction target must stay unprotected")
	}
}
