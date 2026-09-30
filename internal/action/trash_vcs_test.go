package action

import (
	"context"
	"testing"
)

// TestTrashRefusesAllVCSMetadata: the metadata refusal covers Mercurial,
// Jujutsu and Subversion like git, for the metadata directory and anything
// inside it.
func TestTrashRefusesAllVCSMetadata(t *testing.T) {
	for _, meta := range []string{".git", ".hg", ".jj", ".svn"} {
		for _, rel := range []string{"proj/" + meta, "proj/" + meta + "/store/data"} {
			t.Run(rel, func(t *testing.T) {
				fx := newTrashFixture(t)
				fx.env.Force = true
				p := fx.write(rel, "x")
				_, err := trashAction{}.Plan(context.Background(), fx.env, trashFinding(p))
				wantSkip(t, err, "VCS metadata")
			})
		}
	}
}

// TestRestoreRefusesVCSMetadata: undo must not write into any VCS metadata.
func TestRestoreRefusesVCSMetadata(t *testing.T) {
	fx := newTrashFixture(t)
	for _, meta := range []string{".hg", ".jj", ".svn"} {
		if err := refuseRestoreTarget(fx.path("proj/" + meta + "/hooks/x")); err == nil {
			t.Errorf("%s: restore destination not refused", meta)
		}
	}
}
