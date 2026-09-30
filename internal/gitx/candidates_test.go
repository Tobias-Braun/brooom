package gitx_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Tobias-Braun/brooom/internal/gitx"
	"github.com/Tobias-Braun/brooom/internal/testutil"
)

func candidateRefs(bases []gitx.Base) []string {
	var out []string
	for _, b := range bases {
		out = append(out, b.Display())
	}
	return out
}

func TestBaseCandidates(t *testing.T) {
	ctx := context.Background()
	r := execRunner(t)
	tests := []struct {
		name    string
		setup   func(t testing.TB) *testutil.Repo
		conf    []string
		want    []string
		wantErr error
	}{
		{
			name:  "remote primary first, local main marked as not pushed",
			setup: testutil.NewRepoWithRemote,
			conf:  []string{"main", "master"},
			want:  []string{"origin/main", "local main (not pushed)"},
		},
		{
			name:  "no remote: only the local base, not marked",
			setup: testutil.NewRepo,
			conf:  []string{"main", "master"},
			want:  []string{"main"},
		},
		{
			name: "several configured names, duplicates dropped",
			setup: func(t testing.TB) *testutil.Repo {
				repo := testutil.NewRepoWithRemote(t)
				repo.Git("branch", "master", "main")
				return repo
			},
			conf: []string{"main", "master", "main"},
			want: []string{"origin/main", "local main (not pushed)", "local master (not pushed)"},
		},
		{
			name:    "nothing matches",
			setup:   testutil.NewRepo,
			conf:    []string{"develop"},
			wantErr: gitx.ErrNoBase,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo := tc.setup(t)
			got, err := openRepo(t, r, repo.Dir).BaseCandidates(ctx, tc.conf)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if tc.wantErr == nil && !reflect.DeepEqual(candidateRefs(got), tc.want) {
				t.Fatalf("candidates = %v, want %v", candidateRefs(got), tc.want)
			}
		})
	}
}

// TestMergedIntoAnyPrefersFirstMatchingBase: a branch merged only into the
// unpushed local main matches that candidate; once origin/main has the merge
// too, the primary (remote) base is the one reported.
func TestMergedIntoAnyPrefersFirstMatchingBase(t *testing.T) {
	ctx := context.Background()
	r := execRunner(t)
	repo := testutil.NewRepoWithRemote(t)
	repo.Git("checkout", "-q", "-b", "feat", "main")
	repo.Commit("f.txt", "f", "work", testutil.BaseTime.Add(time.Hour))
	repo.Checkout("main")
	repo.GitAt(testutil.BaseTime.Add(2*time.Hour), "merge", "-q", "--no-ff", "-m", "merge feat", "feat")

	g := openRepo(t, r, repo.Dir)
	bases, err := g.BaseCandidates(ctx, []string{"main"})
	if err != nil {
		t.Fatal(err)
	}
	base, res, err := g.MergedIntoAny(ctx, bases, "refs/heads/feat", false)
	if err != nil || !res.Merged || base.Display() != "local main (not pushed)" || !base.Unpushed {
		t.Fatalf("base = %+v, res = %+v, err = %v", base, res, err)
	}

	repo.Push("main")
	repo.Fetch()
	g = openRepo(t, r, repo.Dir)
	bases, _ = g.BaseCandidates(ctx, []string{"main"})
	base, res, err = g.MergedIntoAny(ctx, bases, "refs/heads/feat", false)
	if err != nil || !res.Merged || base.Ref != "origin/main" || base.Unpushed {
		t.Fatalf("base = %+v, res = %+v, err = %v", base, res, err)
	}
}

func TestMergedIntoAnyNotMergedAndUnqualified(t *testing.T) {
	ctx := context.Background()
	r := execRunner(t)
	repo := testutil.NewRepoWithRemote(t)
	repo.Git("checkout", "-q", "-b", "feat", "main")
	repo.Commit("f.txt", "f", "work", testutil.BaseTime.Add(time.Hour))
	repo.Checkout("main")

	g := openRepo(t, r, repo.Dir)
	bases, _ := g.BaseCandidates(ctx, []string{"main"})
	if _, res, err := g.MergedIntoAny(ctx, bases, "refs/heads/feat", true); err != nil || res.Merged {
		t.Fatalf("res = %+v, err = %v; an unmerged branch must not match any base", res, err)
	}
	// An error is unknown, never merged, and is surfaced when nothing matched.
	if _, res, err := g.MergedIntoAny(ctx, bases, "feat", false); err == nil || res.Merged {
		t.Fatalf("res = %+v, err = %v; want the unqualified-ref error", res, err)
	}
}

func TestRefusedBranchName(t *testing.T) {
	for _, tc := range []struct {
		name string
		want string
	}{
		{"feat/x", ""},
		{"fix-1", ""},
		{"-evil", "starts with '-'"},
		{"refs/heads/x", "must not start with refs/"},
		{"a@{1}", "invalid branch name"},
		{"@", "invalid branch name"},
		{"HEAD", "invalid branch name"},
		{"a\nb", "control characters"},
	} {
		got := gitx.RefusedBranchName(tc.name)
		if (tc.want == "") != (got == "") || !strings.Contains(got, tc.want) {
			t.Errorf("RefusedBranchName(%q) = %q, want it to contain %q", tc.name, got, tc.want)
		}
	}
}
