//go:build !windows

package scope

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Tobias-Braun/brooom/internal/testutil"
)

// linkTree extends the base tree with every symlink shape the guard must
// handle. All links live inside root/allowed unless noted.
type linkTree struct {
	*tree
	// nested is a directory below allowed/in that holds a deep relative link.
	nested string
}

func newLinkTree(t *testing.T) *linkTree {
	t.Helper()
	requireSymlink(t)
	tr := newTree(t)
	lt := &linkTree{tree: tr, nested: mkdir(t, tr.in, "deep", "er")}
	mkdir(t, tr.outside, "dir")
	a := tr.allowed
	symlink(t, "../outside", filepath.Join(a, "rel-out"))
	symlink(t, tr.outside, filepath.Join(a, "abs-out"))
	symlink(t, filepath.Join(tr.outside, "secret"), filepath.Join(a, "file-out"))
	symlink(t, "in", filepath.Join(a, "link-in"))
	symlink(t, "abs-out", filepath.Join(a, "chain-1"))
	symlink(t, "chain-1", filepath.Join(a, "chain-2"))
	symlink(t, "chain-2", filepath.Join(a, "chain-3"))
	symlink(t, "link-in", filepath.Join(a, "chain-ok-2"))
	symlink(t, "chain-ok-2", filepath.Join(a, "chain-ok-3"))
	symlink(t, "loop-b", filepath.Join(a, "loop-a"))
	symlink(t, "loop-a", filepath.Join(a, "loop-b"))
	symlink(t, "self", filepath.Join(a, "self"))
	symlink(t, filepath.Join(tr.outside, "new"), filepath.Join(a, "dangling-out"))
	symlink(t, filepath.Join(a, "in", "new"), filepath.Join(a, "dangling-in"))
	symlink(t, "../../../../outside", filepath.Join(lt.nested, "deep-rel-out"))
	symlink(t, filepath.Join(tr.outside, "dir"), filepath.Join(a, "dir-out"))
	symlink(t, filepath.Join(a, "in", "sub"), filepath.Join(tr.outside, "into-allowed"))
	symlink(t, "dir-out/../..", filepath.Join(a, "tricky"))
	return lt
}

func TestResolveSymlinks(t *testing.T) {
	lt := newLinkTree(t)
	a := lt.allowed
	sub := filepath.Join(lt.in, "sub")
	tests := []struct {
		name string
		path string
		// want empty means: refused with ErrOutsideScope.
		want string
	}{
		{"link out (relative)", filepath.Join(a, "rel-out"), ""},
		{"link out (absolute)", filepath.Join(a, "abs-out"), ""},
		{"file link out", filepath.Join(a, "file-out"), ""},
		{"child through link out", filepath.Join(a, "rel-out", "secret"), ""},
		{"chain ending outside", filepath.Join(a, "chain-3"), ""},
		{"deep relative link out", filepath.Join(lt.nested, "deep-rel-out"), ""},
		{"dangling link to outside", filepath.Join(a, "dangling-out"), ""},
		{"child of dangling link to outside", filepath.Join(a, "dangling-out", "x"), ""},
		{"link inside to inside", filepath.Join(a, "link-in"), lt.in},
		{"child through link inside", filepath.Join(a, "link-in", "sub"), sub},
		{"chain ending inside", filepath.Join(a, "chain-ok-3"), lt.in},
		{"dangling link to non-existent inside", filepath.Join(a, "dangling-in"), filepath.Join(lt.in, "new")},
		{"child of dangling link inside", filepath.Join(a, "dangling-in", "x"), filepath.Join(lt.in, "new", "x")},
		{"link outside pointing inside", filepath.Join(lt.outside, "into-allowed"), sub},
		{"link outside pointing inside, child", filepath.Join(lt.outside, "into-allowed", "file"), filepath.Join(sub, "file")},
		{"link then dotdot leaves the link's target parent", filepath.Join(a, "dir-out") + "/..", ""},
		{"link to inside then dotdot", filepath.Join(a, "link-in", "sub", ".."), lt.in},
		{"dotdot after link-in stays inside", filepath.Join(a, "link-in") + "/..", a},
		{"link with dotdot target climbing out", filepath.Join(a, "tricky"), ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := lt.guard.Resolve(tc.path)
			if tc.want == "" {
				if !errors.Is(err, ErrOutsideScope) {
					t.Fatalf("Resolve(%q) = %q, %v; want ErrOutsideScope", tc.path, got, err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("Resolve(%q) = %q, %v; want %q", tc.path, got, err, tc.want)
			}
		})
	}
}

// TestLinkDotDotUsesOSSemantics pins the case a lexical Clean gets wrong:
// "link/.." is the parent of the link's TARGET, not the link's own directory.
func TestLinkDotDotUsesOSSemantics(t *testing.T) {
	lt := newLinkTree(t)
	viaLink := filepath.Join(lt.allowed, "dir-out") + "/.."
	if got, err := lt.guard.Resolve(viaLink); !errors.Is(err, ErrOutsideScope) {
		t.Fatalf("Resolve(%q) = %q, %v; lexically this is the allowed root, but the OS goes to %q", viaLink, got, err, lt.outside)
	}
	// The mirror image: a link outside whose target is inside, and ".." from
	// there stays inside even though the lexical path leaves the scope.
	viaOutside := filepath.Join(lt.outside, "into-allowed") + "/.."
	if got, err := lt.guard.Resolve(viaOutside); err != nil || got != lt.in {
		t.Fatalf("Resolve(%q) = %q, %v; want %q", viaOutside, got, err, lt.in)
	}
}

func TestResolveLoopsFailFast(t *testing.T) {
	lt := newLinkTree(t)
	for _, name := range []string{"loop-a", "loop-b", "self"} {
		t.Run(name, func(t *testing.T) {
			done := make(chan error, 1)
			go func() {
				_, err := lt.guard.Resolve(filepath.Join(lt.allowed, name))
				done <- err
			}()
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("a link loop must be refused")
				}
				if errors.Is(err, ErrOutsideScope) {
					t.Errorf("a loop is not a scope violation, the message must say so: %v", err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("resolution of a link loop hangs")
			}
		})
	}
	if lt.guard.IsAllowedRoot(filepath.Join(lt.allowed, "self")) {
		t.Error("IsAllowedRoot of a loop must be false")
	}
}

func TestResolveHopLimit(t *testing.T) {
	requireSymlink(t)
	tr := newTree(t)
	// A chain of maxSymlinkHops links resolves; one more is refused.
	build := func(n int) string {
		dir := mkdir(t, tr.allowed, fmt.Sprintf("chain%d", n))
		symlink(t, "../in", filepath.Join(dir, "l0"))
		for i := 1; i <= n; i++ {
			symlink(t, fmt.Sprintf("l%d", i-1), filepath.Join(dir, fmt.Sprintf("l%d", i)))
		}
		return filepath.Join(dir, fmt.Sprintf("l%d", n))
	}
	if got, err := tr.guard.Resolve(build(maxSymlinkHops - 1)); err != nil || got != tr.in {
		t.Errorf("chain within the hop limit = %q, %v; want %q", got, err, tr.in)
	}
	if got, err := tr.guard.Resolve(build(maxSymlinkHops + 5)); err == nil {
		t.Errorf("chain beyond the hop limit = %q, want error", got)
	}
}

func TestSymlinkedAllowedRoot(t *testing.T) {
	requireSymlink(t)
	tr := newTree(t)
	link := filepath.Join(tr.root, "root-link")
	symlink(t, tr.allowed, link)
	g, err := NewGuard(link)
	if err != nil {
		t.Fatal(err)
	}
	if got := g.Allowed(); len(got) != 1 || got[0] != tr.allowed {
		t.Fatalf("Allowed() = %q, want the resolved %q", got, tr.allowed)
	}
	for _, p := range []string{link, filepath.Join(link, "in"), filepath.Join(tr.allowed, "in")} {
		if _, err := g.Resolve(p); err != nil {
			t.Errorf("Resolve(%q): %v", p, err)
		}
	}
	if !g.IsAllowedRoot(link) || !g.IsAllowedRoot(tr.allowed) {
		t.Error("the root reached through its link and directly must both be the allowed root")
	}
	if _, err := g.Resolve(filepath.Join(link, "..", "outside")); !errors.Is(err, ErrOutsideScope) {
		t.Errorf("escaping via the linked root = %v, want ErrOutsideScope", err)
	}
}

func TestResolveThroughSymlinkedCwd(t *testing.T) {
	lt := newLinkTree(t)
	t.Chdir(filepath.Join(lt.allowed, "link-in"))
	got, err := lt.guard.Resolve("sub")
	if want := filepath.Join(lt.in, "sub"); err != nil || got != want {
		t.Fatalf("Resolve(sub) = %q, %v; want %q", got, err, want)
	}
}

func TestResolveParentSymlinks(t *testing.T) {
	lt := newLinkTree(t)
	a := lt.allowed
	sep := string(os.PathSeparator)
	tests := []struct {
		name string
		path string
		// want empty means an error is expected.
		want string
	}{
		{"link pointing out is kept unresolved", filepath.Join(a, "rel-out"), filepath.Join(a, "rel-out")},
		{"link pointing out, trailing separator", filepath.Join(a, "rel-out") + sep, filepath.Join(a, "rel-out")},
		{"dangling link", filepath.Join(a, "dangling-out"), filepath.Join(a, "dangling-out")},
		{"loop link", filepath.Join(a, "loop-a"), filepath.Join(a, "loop-a")},
		{"link inside kept unresolved", filepath.Join(a, "link-in"), filepath.Join(a, "link-in")},
		{"parent is a link inside", filepath.Join(a, "link-in", "sub"), filepath.Join(lt.in, "sub")},
		{"parent is a link outside", filepath.Join(a, "rel-out", "secret"), ""},
		{"parent is a dangling link outside", filepath.Join(a, "dangling-out", "x"), ""},
		{"parent is a loop", filepath.Join(a, "loop-a", "x"), ""},
		{"link to allowed's parent then dotdot", filepath.Join(a, "link-in") + sep + "..", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := lt.guard.ResolveParent(tc.path)
			if tc.want == "" {
				if err == nil {
					t.Fatalf("ResolveParent(%q) = %q, want error", tc.path, got)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("ResolveParent(%q) = %q, %v; want %q", tc.path, got, err, tc.want)
			}
		})
	}
}

func TestUnixSpecialNames(t *testing.T) {
	tr := newTree(t)
	// Colons and backslashes are ordinary characters in unix file names, so
	// the Windows-only refusals must not apply here.
	for _, name := range []string{`a:b`, `back\slash`, "trailing.", "trailing "} {
		p := filepath.Join(tr.in, name)
		if got, err := tr.guard.Resolve(p); err != nil || got != p {
			t.Errorf("Resolve(%q) = %q, %v; want it unchanged", p, got, err)
		}
	}
}

func TestResolvePermissionErrorIsRefusal(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	tr := newTree(t)
	locked := mkdir(t, tr.in, "locked")
	touch(t, locked, "inner")
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	got, err := tr.guard.Resolve(filepath.Join(locked, "inner"))
	if err == nil {
		t.Fatalf("Resolve below an unreadable directory = %q, want a refusal", got)
	}
	if errors.Is(err, ErrOutsideScope) {
		t.Errorf("a permission error must not be reported as a scope violation: %v", err)
	}
}

func TestRelativeLinkResolvesAgainstLinkDirectory(t *testing.T) {
	requireSymlink(t)
	tr := newTree(t)
	// allowed/in/sub/up -> ../../../outside climbs to root/outside because
	// the target is relative to allowed/in/sub, not to the working directory.
	symlink(t, "../../../outside", filepath.Join(tr.in, "sub", "up"))
	t.Chdir(tr.outside)
	if got, err := tr.guard.Resolve(filepath.Join(tr.in, "sub", "up")); !errors.Is(err, ErrOutsideScope) {
		t.Fatalf("Resolve = %q, %v; want ErrOutsideScope", got, err)
	}
	symlink(t, "../../in", filepath.Join(tr.in, "sub", "back"))
	if got, err := tr.guard.Resolve(filepath.Join(tr.in, "sub", "back")); err != nil || got != tr.in {
		t.Fatalf("Resolve = %q, %v; want %q", got, err, tr.in)
	}
}

func TestNewGuardRefusesLinkToRoot(t *testing.T) {
	requireSymlink(t)
	dir := testutil.ResolvedTempDir(t)
	link := filepath.Join(dir, "to-root")
	symlink(t, volumeRoot(t), link)
	if g, err := NewGuard(link); err == nil {
		t.Fatalf("NewGuard(link to /) = %v, want error", g.Allowed())
	}
}
