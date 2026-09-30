package mergedbranch_test

import (
	"strings"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/findings"
)

// TestMergeIntoUnpushedLocalMainIsReported: origin/HEAD points at origin/main,
// but the merge exists only in a local main that is ahead of it. The single
// primary base used to miss it; the finding now names the matching ref.
func TestMergeIntoUnpushedLocalMainIsReported(t *testing.T) {
	f := newFixture(t)
	f.feature("feat/a", "a.txt")
	f.merge("feat/a")

	x := mustFind(t, f.detect(), "feat/a")
	if x.SuggestedAction.Type != findings.ActionDeleteBranch {
		t.Fatalf("action = %+v", x.SuggestedAction)
	}
	if !strings.Contains(x.SuggestedAction.Reason, "local main (not pushed)") {
		t.Errorf("reason = %q, want it to name the local base", x.SuggestedAction.Reason)
	}
	if x.Meta["base"] != "main" {
		t.Errorf("base = %q", x.Meta["base"])
	}
	if !strings.Contains(x.Evidence[0].Message, "local main (not pushed)") {
		t.Errorf("evidence = %+v", x.Evidence[0])
	}
}

// TestMergeIntoRemoteBaseStaysPreferred: once the merge is pushed, origin/main
// is the reported base again, without the local qualifier.
func TestMergeIntoRemoteBaseStaysPreferred(t *testing.T) {
	f := newFixture(t)
	f.feature("feat/a", "a.txt")
	f.merge("feat/a")
	f.publish()

	x := mustFind(t, f.detect(), "feat/a")
	if x.Meta["base"] != "origin/main" || strings.Contains(x.SuggestedAction.Reason, "not pushed") {
		t.Fatalf("meta = %v, reason = %q", x.Meta, x.SuggestedAction.Reason)
	}
}

// TestRefusedNamesAreVisibleButNotActionable: refs the delete-branch action
// refuses at plan time must not be counted as actionable findings.
func TestRefusedNamesAreVisibleButNotActionable(t *testing.T) {
	f := newFixture(t)
	f.feature("feat/a", "a.txt")
	f.merge("feat/a")
	f.publish()
	tip := f.repo.Git("rev-parse", "feat/a")
	f.repo.Git("update-ref", "refs/heads/-evil", tip)
	f.repo.Git("update-ref", "refs/heads/HEAD", tip)

	got := f.detect()
	for _, name := range []string{"-evil", "HEAD"} {
		x := mustFind(t, got, name)
		if x.Actionable() || x.SuggestedAction.Command != "" {
			t.Errorf("%s: action = %+v, want none", name, x.SuggestedAction)
		}
		want := "git update-ref -d refs/heads/" + name
		if !strings.Contains(x.SuggestedAction.Reason, want) {
			t.Errorf("%s: reason %q lacks %q", name, x.SuggestedAction.Reason, want)
		}
	}
	if !mustFind(t, got, "feat/a").Actionable() {
		t.Error("a normal branch lost its action")
	}
}
