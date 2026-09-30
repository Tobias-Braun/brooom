package action

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/procs"
)

// stubOpenFiles replaces the open-file check for one test.
func stubOpenFiles(t *testing.T, fn func(context.Context, []string) (map[string]bool, error)) {
	t.Helper()
	old := openFilesFn
	openFilesFn = fn
	t.Cleanup(func() { openFilesFn = old })
}

// TestRemoveWorktreeRefusesInUse covers issue #100: a worktree a process has
// open, or that contains the current directory, is never removed, with or
// without --force, for clean and dirty worktrees alike and in Plan as well as
// Apply (a forged or stale finding reaches Apply too).
func TestRemoveWorktreeRefusesInUse(t *testing.T) {
	tests := []struct {
		name  string
		dirty bool
		force bool
		setup func(t *testing.T, path string)
		want  string
	}{
		{"open file, clean", false, false, func(t *testing.T, path string) {
			stubOpenFiles(t, func(_ context.Context, paths []string) (map[string]bool, error) {
				return map[string]bool{paths[0]: true}, nil
			})
		}, "open by a process"},
		{"open file, dirty, forced", true, true, func(t *testing.T, path string) {
			stubOpenFiles(t, func(_ context.Context, paths []string) (map[string]bool, error) {
				return map[string]bool{paths[0]: true}, nil
			})
		}, "open by a process"},
		{"cwd inside worktree, clean", false, false, func(t *testing.T, path string) {
			stubOpenFiles(t, func(context.Context, []string) (map[string]bool, error) { return nil, nil })
			sub := filepath.Join(path, "sub")
			if err := os.MkdirAll(sub, 0o755); err != nil {
				t.Fatal(err)
			}
			t.Chdir(sub)
		}, "current directory"},
		{"cwd is worktree root, dirty, forced", true, true, func(t *testing.T, path string) {
			stubOpenFiles(t, func(context.Context, []string) (map[string]bool, error) { return nil, nil })
			t.Chdir(path)
		}, "current directory"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fx := newWTFixture(t)
			path := fx.add("wt", "feat")
			f := fx.removeFinding(path)
			if tt.dirty {
				if err := os.WriteFile(filepath.Join(path, "scratch.txt"), []byte("x"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			fx.env.Force = tt.force
			tt.setup(t, path)

			_, err := fx.plan(removeWorktree{}, f)
			if !errors.Is(err, ErrSkipped) {
				t.Fatalf("Plan err = %v, want a skip", err)
			}
			wantContains(t, err.Error(), tt.want)

			// Apply re-validates on its own; feed it the step a stale plan would hold.
			en, err := removeWorktree{}.Apply(context.Background(), fx.env, Step{Finding: f})
			if err != nil {
				t.Fatal(err)
			}
			if en.Status != "skipped" {
				t.Errorf("Apply status = %q, want skipped", en.Status)
			}
			if !exists(path) || !fx.registered(path) {
				t.Error("in-use worktree was removed")
			}
		})
	}
}

// TestRemoveWorktreeUnknownOpenCheckIsNoted keeps the trash semantics: an
// unavailable check is unknown, allowed, but visible in the plan.
func TestRemoveWorktreeUnknownOpenCheckIsNoted(t *testing.T) {
	fx := newWTFixture(t)
	path := fx.add("wt", "feat")
	stubOpenFiles(t, func(context.Context, []string) (map[string]bool, error) {
		return nil, procs.ErrUnavailable
	})
	step, err := fx.plan(removeWorktree{}, fx.removeFinding(path))
	if err != nil {
		t.Fatal(err)
	}
	wantContains(t, step.Description, "open-file check unavailable")
}
