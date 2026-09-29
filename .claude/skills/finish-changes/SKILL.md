---
name: finish-changes
description: Finish the current uncommitted changes - commit them on a new type/topic branch, push, open a GitHub PR (via gh or the GitHub MCP server) and merge it once CI succeeds (no --auto), then clean up a worktree. Use when the user says "finish the changes", "finishen", or asks to commit, push and open a PR for the work in progress.
---

# Finish changes

Turns the uncommitted work in the current repository into a pull request that is merged once CI is green.

## Steps

1. **Inspect the state.** Run `git status` and `git diff` (staged and unstaged) to understand what changed. If there is nothing to commit, say so and stop.

2. **Pick the branch name** in the form `<type>/<what-has-been-changed>`:
   - `type` is the best fitting one of: `feat` (new capability), `fix` (bug fix), `enhance` (improvement of existing behaviour), `refactor` (restructuring without behaviour change), `perf` (performance), `doc` (documentation only), `test` (tests only), `style` (formatting, no logic change), `build` (build system, dependencies), `ci` (CI/CD pipelines), `chore` (maintenance, config, tooling), `revert` (undoing an earlier change).
   - The second part is a short lowercase kebab-case summary of the change, e.g. `feat/my-super-cool-feature`, `fix/bug-in-user-view`.
   - If the current branch is the default branch (or otherwise not a branch created for this work), create the new branch with `git switch -c <name>`. If already on a branch that follows this pattern, keep using it.

3. **Commit** through the `git` CLI as the configured git user. Stage the relevant files explicitly (no secrets, no unrelated files). Write a concise imperative commit message describing the why. Follow the global rules: no `Co-Authored-By` trailer and no Claude attribution, and never override `user.email`.

4. **Push** with `git push -u origin <branch>`.

5. **Check whether the repo matches a GitHub repo**:
   - Read `git remote get-url origin`. It must point to `github.com`; otherwise there is no GitHub repo.
   - The repository name in the remote (`owner/<repo>`) must match the name of the local repository folder 1:1 (`basename "$(git rev-parse --show-toplevel)"`), and the repo must be reachable: `gh repo view <owner>/<repo>` succeeds, or, when `gh` is missing or not authenticated (e.g. cloud sessions), the GitHub MCP server can read it. Remember which of the two works; steps 6-7 use the same one (`gh` preferred).
   - If any of this fails, tell the user that the repository does not match a GitHub repository, skip all PR actions and end the task (the local commit stays as is).

6. **Create the PR** with `gh pr create`, or with the GitHub MCP server's `create_pull_request` tool when `gh` is unavailable (never via the web UI). Title: a short summary of the change. Body: what changed and why, kept brief. No Claude attribution lines.

7. **Merge once CI succeeds.** Do NOT use `gh pr merge --auto` (or enable auto-merge via MCP): auto-merge is not available in the user's repositories. Instead wait for the checks with `gh pr checks <pr> --watch`, then merge with `gh pr merge <pr> --merge`. Without `gh`, poll the PR's check status through the GitHub MCP server (e.g. `pull_request_read` with its status/check-runs method) until all checks finished, then call its `merge_pull_request` tool with merge method `merge`. Use `--merge` because this repository's history consists of merge commits; use the strategy the repo already uses if it differs. If the PR has no checks, merge right away. If a check fails, do not merge: report the failure and stop.

8. **Clean up the worktree.** If the changes were made in an active git worktree (`git rev-parse --git-common-dir` differs from `git rev-parse --git-dir`), remove it after the merge succeeded: leave the worktree directory (e.g. `cd` to the main checkout from `git worktree list`), run `git worktree remove <path>`, then delete the merged local branch with `git branch -d <branch>`. Skip this if the merge did not happen.

9. **Report** the branch name, the PR URL, the merge result and whether a worktree was removed.
