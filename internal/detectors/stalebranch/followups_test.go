package stalebranch

import (
	"strings"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/findings"
)

// TestBranchMergedIntoUnpushedLocalMainIsNotStale: the branch is pushed and
// was merged into a local main that is ahead of origin/main. merged-branch owns
// merged branches, but stale-branch only asked the primary base, so it
// reported the branch as one that was never merged.
func TestBranchMergedIntoUnpushedLocalMainIsNotStale(t *testing.T) {
	f := newFixture(t, true)
	f.pushed("old", f.daysAgo(100))
	f.repo.GitAt(f.daysAgo(90), "merge", "-q", "--no-ff", "-m", "merge old", "old")

	if out := f.mustDetect(); len(out) != 0 {
		t.Fatalf("merged branch reported as stale: %+v", out)
	}
}

// TestRefusedNamesAreNotActionable: names the delete-branch action refuses
// used to be counted as actionable and then skipped at plan time.
func TestRefusedNamesAreNotActionable(t *testing.T) {
	f := newFixture(t, true)
	f.pushed("old", f.daysAgo(100))
	tip := f.repo.Git("rev-parse", "old")
	f.repo.Git("update-ref", "refs/heads/-evil", tip)
	f.repo.Git("update-ref", "refs/heads/HEAD", tip)

	byRef := map[string]findings.Finding{}
	for _, x := range f.mustDetect() {
		byRef[x.Ref] = x
	}
	if !byRef["old"].Actionable() {
		t.Fatalf("control branch lost its action: %+v", byRef["old"])
	}
	for _, name := range []string{"-evil", "HEAD"} {
		x, ok := byRef[name]
		if !ok {
			t.Fatalf("%s is not reported", name)
		}
		if x.Actionable() || x.SuggestedAction.Command != "" {
			t.Errorf("%s: action = %+v, want none", name, x.SuggestedAction)
		}
		if want := "git update-ref -d refs/heads/" + name; !strings.Contains(x.SuggestedAction.Reason, want) {
			t.Errorf("%s: reason %q lacks %q", name, x.SuggestedAction.Reason, want)
		}
	}
}
