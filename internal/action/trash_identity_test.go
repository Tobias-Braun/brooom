package action

import (
	"path/filepath"
	"strings"
	"testing"
)

// aliasing makes isSameEntry treat two spellings as one object, as an 8.3
// short name does on Windows, and restores the real comparison afterwards.
func aliasing(t *testing.T, alias, real string) {
	t.Helper()
	old := isSameEntry
	isSameEntry = func(a, b string) bool {
		if strings.EqualFold(a, alias) && strings.EqualFold(b, real) {
			return true
		}
		return old(a, b)
	}
	t.Cleanup(func() { isSameEntry = old })
}

func TestTrashRefusesShortNameAliases(t *testing.T) {
	tests := []struct {
		name  string
		build func(fx *trashFixture) (path, alias, real string)
		want  string
	}{
		{"alias of .git", func(fx *trashFixture) (string, string, string) {
			fx.mkdir("repo/.git")
			p := fx.path("repo/GIT~1")
			return p, p, fx.path("repo/.git")
		}, ".git"},
		{"inside an alias of .git", func(fx *trashFixture) (string, string, string) {
			fx.mkdir("repo/.git/objects")
			return fx.path("repo/GIT~1/objects"), fx.path("repo/GIT~1"), fx.path("repo/.git")
		}, ".git"},
		{"alias of the quarantine", func(fx *trashFixture) (string, string, string) {
			fx.mkdir("brooom-home/quarantine")
			p := filepath.Join(fx.brooom, "QUARAN~1")
			return p, p, filepath.Join(fx.brooom, "quarantine")
		}, "session or quarantine"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fx := newTrashFixture(t)
			path, alias, real := tc.build(fx)
			// Without an aliasing file system the path does not exist, so
			// the fake identity is what makes the refusal reachable.
			aliasing(t, alias, real)
			err := RefuseByIdentity(path)
			wantSkip(t, err, tc.want)
			wantSkip(t, refuseTarget(fx.env, path), tc.want)
		})
	}
}

func TestRefuseByIdentityAllowsOrdinaryPaths(t *testing.T) {
	fx := newTrashFixture(t)
	p := fx.mkdir("proj/node_modules")
	if err := RefuseByIdentity(p); err != nil {
		t.Fatalf("ordinary directory refused: %v", err)
	}
}

func TestRefuseByIdentityProtectedHomes(t *testing.T) {
	fx := newTrashFixture(t)
	fx.mkdir("brooom-home/sessions")
	for _, p := range []string{fx.userHome, filepath.Join(fx.brooom, "sessions")} {
		if err := RefuseByIdentity(p); err == nil {
			t.Errorf("%s was not refused by identity", p)
		}
	}
}
