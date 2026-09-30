package gitx_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/gitx"
	"github.com/Tobias-Braun/brooom/internal/testutil"
)

func TestDefaultBase(t *testing.T) {
	ctx := context.Background()
	r := execRunner(t)
	configured := []string{"main", "master"}

	tests := []struct {
		name    string
		setup   func(t testing.TB) *testutil.Repo
		conf    []string
		want    gitx.Base
		wantErr error
	}{
		{
			name:  "origin/HEAD wins",
			setup: testutil.NewRepoWithRemote,
			conf:  []string{"master"},
			want:  gitx.Base{Ref: "origin/main", FullRef: "refs/remotes/origin/main", Name: "main", Remote: "origin", Source: gitx.BaseSourceOriginHead},
		},
		{
			name: "no origin/HEAD, configured remote ref",
			setup: func(t testing.TB) *testutil.Repo {
				repo := testutil.NewRepoWithRemote(t)
				repo.Git("remote", "set-head", "origin", "-d")
				return repo
			},
			conf: configured,
			want: gitx.Base{Ref: "origin/main", FullRef: "refs/remotes/origin/main", Name: "main", Remote: "origin", Source: gitx.BaseSourceConfigRemote},
		},
		{
			name: "dangling origin/HEAD falls back to local",
			setup: func(t testing.TB) *testutil.Repo {
				repo := testutil.NewRepoWithRemote(t)
				repo.Git("update-ref", "-d", "refs/remotes/origin/main")
				return repo
			},
			conf: configured,
			want: gitx.Base{Ref: "main", FullRef: "refs/heads/main", Name: "main", Source: gitx.BaseSourceConfigLocal},
		},
		{
			name:  "no remote, local main",
			setup: testutil.NewRepo,
			conf:  configured,
			want:  gitx.Base{Ref: "main", FullRef: "refs/heads/main", Name: "main", Source: gitx.BaseSourceConfigLocal},
		},
		{
			name: "only master",
			setup: func(t testing.TB) *testutil.Repo {
				repo := testutil.NewRepo(t)
				repo.Git("branch", "-m", "main", "master")
				return repo
			},
			conf: configured,
			want: gitx.Base{Ref: "master", FullRef: "refs/heads/master", Name: "master", Source: gitx.BaseSourceConfigLocal},
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
			got, err := openRepo(t, r, repo.Dir).DefaultBase(ctx, tc.conf)
			if !errors.Is(err, tc.wantErr) || got != tc.want {
				t.Fatalf("DefaultBase = %+v, %v; want %+v, %v", got, err, tc.want, tc.wantErr)
			}
		})
	}
}

func TestDefaultBaseIsReadOnly(t *testing.T) {
	r := execRunner(t)
	repo := testutil.NewRepoWithRemote(t)
	repo.Git("remote", "set-head", "origin", "-d")
	if _, err := openRepo(t, r, repo.Dir).DefaultBase(context.Background(), []string{"main"}); err != nil {
		t.Fatal(err)
	}
	if out := repo.Git("for-each-ref", "refs/remotes/origin/HEAD"); out != "" {
		t.Errorf("DefaultBase must not run remote set-head, found %q", out)
	}
}

func TestIsProtected(t *testing.T) {
	patterns := []string{"main", "release/*", "hotfix/**", "[bad"}
	tests := []struct {
		branch string
		want   bool
	}{
		{"main", true},
		{"Main", false},
		{"release/1.0", true},
		{"release/1.0/hotfix", false},
		{"release", false},
		{"hotfix/a/b/c", true},
		{"hotfix", false},
		{"[bad", true},
		{"feat/x", false},
	}
	for _, tc := range tests {
		if got := gitx.IsProtected(patterns, tc.branch); got != tc.want {
			t.Errorf("IsProtected(%q) = %v, want %v", tc.branch, got, tc.want)
		}
	}
	if gitx.IsProtected(nil, "main") {
		t.Error("no patterns must protect nothing")
	}
}

func TestIsBaseBranch(t *testing.T) {
	base := gitx.Base{Ref: "origin/trunk", Name: "trunk", Remote: "origin"}
	conf := []string{"main", "master"}
	for name, want := range map[string]bool{"trunk": true, "main": true, "master": true, "feat": false} {
		if got := gitx.IsBaseBranch(base, conf, name); got != want {
			t.Errorf("IsBaseBranch(%q) = %v, want %v", name, got, want)
		}
	}
	if gitx.IsBaseBranch(gitx.Base{}, nil, "") {
		t.Error("empty base must not match the empty name")
	}
}

func TestSamePath(t *testing.T) {
	tests := []struct {
		goos string
		a, b string
		want bool
	}{
		{"windows", `C:\Users\Dev\Repo`, `c:\users\dev\repo`, true},
		{"windows", `C:/Users/Dev/Repo`, `C:/Users/Dev/Repo/`, true},
		{"darwin", "/Users/Dev/Repo", "/users/dev/repo", true},
		{"linux", "/home/Dev/Repo", "/home/dev/repo", false},
		{"linux", "/home/dev/a/../repo", "/home/dev/repo", true},
		{"linux", "/home/dev/repo", "/home/dev/other", false},
	}
	for _, tc := range tests {
		if got := gitx.SamePathOS(tc.goos, tc.a, tc.b); got != tc.want {
			t.Errorf("SamePathOS(%s, %q, %q) = %v, want %v", tc.goos, tc.a, tc.b, got, tc.want)
		}
	}
	if !gitx.SamePath("/x/y", "/x/y/") {
		t.Error("SamePath must ignore trailing separators")
	}
}
