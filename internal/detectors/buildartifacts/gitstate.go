package buildartifacts

import (
	"context"
	"errors"
	"path/filepath"
	"strings"

	"github.com/Tobias-Braun/brooom/internal/gitx"
	"github.com/Tobias-Braun/brooom/internal/scope"
)

// gitState is what git says about one candidate. All of it comes from
// read-only commands (gitx sets GIT_OPTIONAL_LOCKS=0).
type gitState struct {
	// tracked: git tracks at least one file below the candidate.
	tracked bool
	// ignored is only meaningful when ignoreKnown is true.
	ignored     bool
	ignoreKnown bool
}

// literalPathspec makes git treat a path literally, so directory names with
// glob characters ("[id]", "*.egg-info") are never expanded.
func literalPathspec(rel string) string { return ":(literal)" + rel }

func joinPath(root, rel string) string { return filepath.Join(root, filepath.FromSlash(rel)) }

// gitStateOf asks git about the candidate at rel. Targets that are not git
// repositories have no git state, so neither tracked_files nor gitignored
// can apply.
func (s *scan) gitStateOf(ctx context.Context, rel string) gitState {
	if s.target.Kind != scope.TargetRepo {
		return gitState{}
	}
	ignored, known := s.ignoredVerdict(ctx, rel)
	return gitState{tracked: s.isTracked(ctx, rel), ignored: ignored, ignoreKnown: known}
}

// isTracked runs git ls-files for the candidate. A failing command counts as
// tracked: claiming a directory is free of tracked files without having got
// an answer from git would break the blocking-flag guarantee, and the
// trash action re-checks and treats a failing check the same way.
func (s *scan) isTracked(ctx context.Context, rel string) bool {
	out, err := s.env.Git.Run(ctx, s.root, "ls-files", "-z", "--", literalPathspec(rel))
	return err != nil || out != ""
}

// ignoredVerdict runs git check-ignore -q: exit 0 means ignored, exit 1 not
// ignored, anything else is unknown and never fails the scan.
func (s *scan) ignoredVerdict(ctx context.Context, rel string) (ignored, known bool) {
	_, err := s.env.Git.Run(ctx, s.root, "check-ignore", "-q", "--", rel)
	if err == nil {
		return true, true
	}
	var ge *gitx.Error
	if errors.As(err, &ge) && ge.ExitCode == 1 && strings.TrimSpace(ge.Stderr) == "" {
		return false, true
	}
	return false, false
}
