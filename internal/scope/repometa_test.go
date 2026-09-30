package scope_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/scope"
)

// metaGuard allows work and accepts main only as repository metadata.
func metaGuard(t *testing.T) (g *scope.Guard, work, main string) {
	t.Helper()
	base := resolvedTemp(t)
	work, main = filepath.Join(base, "work"), filepath.Join(base, "main")
	for _, d := range []string{work, filepath.Join(main, "sub")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	plain, err := scope.NewGuard(work)
	if err != nil {
		t.Fatal(err)
	}
	if g, err = plain.WithRepoMeta(main); err != nil {
		t.Fatal(err)
	}
	return g, work, main
}

func resolvedTemp(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestResolveRepoMetaAcceptsOnlyTheMetaDirectory(t *testing.T) {
	g, work, main := metaGuard(t)
	for _, tc := range []struct {
		name string
		call func(string) (string, error)
		path string
		ok   bool
	}{
		{"meta dir via ResolveRepoMeta", g.ResolveRepoMeta, main, true},
		{"meta dir via Resolve", g.Resolve, main, false},
		{"below meta via ResolveRepoMeta", g.ResolveRepoMeta, filepath.Join(main, "sub"), false},
		{"below meta via Resolve", g.Resolve, filepath.Join(main, "sub"), false},
		{"allowed via ResolveRepoMeta", g.ResolveRepoMeta, work, true},
		{"allowed via Resolve", g.Resolve, work, true},
		{"elsewhere via ResolveRepoMeta", g.ResolveRepoMeta, filepath.Dir(work), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.call(tc.path)
			if tc.ok {
				if err != nil || got != tc.path {
					t.Fatalf("got %q, %v; want %q", got, err, tc.path)
				}
				return
			}
			if !errors.Is(err, scope.ErrOutsideScope) {
				t.Fatalf("got %q, %v; want ErrOutsideScope", got, err)
			}
		})
	}
}

func TestMetaLocationIsNotAllowedRootOrListed(t *testing.T) {
	g, _, main := metaGuard(t)
	if g.IsAllowedRoot(main) {
		t.Error("meta location counts as an allowed root")
	}
	for _, a := range g.Allowed() {
		if a == main {
			t.Errorf("Allowed() lists the meta location: %v", g.Allowed())
		}
	}
	if g.OutsideNote(main) != scope.OutsideWorktreeHint {
		t.Error("meta location must not count as inside the scope")
	}
}

func TestWithRepoMetaValidatesAndDoesNotMutate(t *testing.T) {
	plain, err := scope.NewGuard(resolvedTemp(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plain.WithRepoMeta(filepath.Join(resolvedTemp(t), "missing")); err == nil {
		t.Error("a missing meta location was accepted")
	}
	other := resolvedTemp(t)
	if _, err := plain.WithRepoMeta(other); err != nil {
		t.Fatal(err)
	}
	if _, err := plain.ResolveRepoMeta(other); !errors.Is(err, scope.ErrOutsideScope) {
		t.Errorf("WithRepoMeta mutated the receiver: %v", err)
	}
}
