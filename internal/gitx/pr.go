package gitx

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"time"
)

// defaultGHTimeout bounds the gh call so an offline machine never stalls a scan.
const defaultGHTimeout = 5 * time.Second

// PRInfo is the set of branches with an open pull request.
type PRInfo struct {
	// Known is false whenever the information could not be obtained (gh
	// missing, unauthenticated, offline, timeout, bad output). Callers must
	// then treat every branch as possibly having a PR only where that is the
	// safe direction, and never abort the scan.
	Known bool
	// Branches holds head branch names of open PRs.
	Branches map[string]bool
}

// HasOpenPR reports whether name is the head of an open PR. It is only
// meaningful when Known is true.
func (p PRInfo) HasOpenPR(name string) bool { return p.Known && p.Branches[name] }

// GHRunner runs `gh args...` in dir with the given extra environment
// entries and returns stdout. It is injectable so tests need no real gh.
type GHRunner func(ctx context.Context, dir string, env []string, args ...string) ([]byte, error)

// PROptions configures OpenPRBranches.
type PROptions struct {
	// GH runs gh; nil uses the gh binary on PATH.
	GH GHRunner
	// Timeout bounds the call; zero means 5 seconds.
	Timeout time.Duration
}

// ghEnv keeps gh non-interactive and free of colour and pager output.
var ghEnv = []string{"GH_PROMPT_DISABLED=1", "NO_COLOR=1", "GH_PAGER=cat"}

// OpenPRBranches lists the head branch names of open pull requests through
// `gh pr list`. This is the only network use in detection; callers gate it on
// the UseGH configuration, not this function. Any failure yields
// PRInfo{Known:false} and no error, so a missing or offline gh never aborts a
// scan. On cached handles a scan-wide breaker stops asking gh after the first
// timeout, network error or missing binary: every later repository gets
// PRInfo{Known:false} immediately. The branch-name-only check ignores forks: a fork PR with the same
// head name is a false positive, which errs on the safe side.
func (r *Repo) OpenPRBranches(ctx context.Context, dir string, opts PROptions) PRInfo {
	return r.sharedPRs(func() (PRInfo, error) {
		return r.gh.do(func() (PRInfo, ghVerdict) { return fetchOpenPRs(ctx, dir, opts) }), nil
	})
}

func fetchOpenPRs(parent context.Context, dir string, opts PROptions) (PRInfo, ghVerdict) {
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = defaultGHTimeout
	}
	gh := opts.GH
	if gh == nil {
		gh = execGH
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	out, err := gh(ctx, dir, ghEnv, "pr", "list", "--state", "open", "--json", "headRefName", "--limit", "500")
	if err == nil && ctx.Err() != nil {
		err = ctx.Err()
	}
	if err != nil {
		return PRInfo{}, classifyGHError(parent, ctx, err)
	}
	var prs []struct {
		HeadRefName string `json:"headRefName"`
	}
	if err := json.Unmarshal(out, &prs); err != nil {
		return PRInfo{}, ghNeutral
	}
	info := PRInfo{Known: true, Branches: make(map[string]bool, len(prs))}
	for _, p := range prs {
		info.Branches[p.HeadRefName] = true
	}
	return info, ghHealthy
}

// execGH runs the gh binary from PATH.
func execGH(ctx context.Context, dir string, env []string, args ...string) ([]byte, error) {
	path, err := exec.LookPath("gh")
	if err != nil {
		return nil, errors.New("gh not found in PATH")
	}
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Dir = dir
	// gh spawns git, which would inherit GIT_DIR, GIT_INDEX_FILE and friends
	// from a hook or direnv and read another repository; use the same
	// sanitized environment as the git calls.
	cmd.Env = append(Env(os.Environ()), env...)
	// A grandchild (gh spawns helpers) may hold the output pipe past the
	// deadline; WaitDelay force-closes it so the timeout is real.
	cmd.WaitDelay = waitDelay
	ownProcessGroup(cmd)
	return cmd.Output()
}
