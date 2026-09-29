---
name: issue
description: Set up work on a new issue (feature, bug, research, ...) or work on an already defined one. Use as "/issue <description of what to achieve>" to interactively plan and record the issue (GitHub issue via gh or the GitHub MCP server, or a local file), and as "/issue <number or file>" to implement a recorded issue and close it afterwards.
---

# Issue

Two modes, chosen by the argument that follows `/issue`:

- **Define** (free-text description of a goal): plan interactively and record the result as an issue.
- **Work** (a GitHub issue number such as `12` / `#12`, or a path to an issue file): implement that issue and close it.

If the argument is ambiguous, ask which mode is meant.

## Define mode

1. **Detect the GitHub repo and access method.** Read `git remote get-url origin`. It counts as a GitHub repo only if it points to `github.com` and the repo is reachable: `gh repo view <owner>/<repo>` succeeds, or, when `gh` is missing or not authenticated (e.g. cloud sessions), the GitHub MCP server can read it (e.g. its `get_file_contents` or repository search tool). Prefer `gh`; use the GitHub MCP server as the alternative. Note the result (GitHub repo yes/no, via `gh` or MCP) and tell the user in one line which recording target will be used.

2. **Enter plan mode** (`EnterPlanMode`). Do not change any project files until the plan is approved.

3. **Refine the issue interactively.** Explore the codebase as needed, then use `AskUserQuestion` to clarify the type (feature / bug / research / chore), scope, acceptance criteria and open design decisions. Iterate until the description is unambiguous. The plan presented via `ExitPlanMode` must contain:
   - **Title** (short, imperative)
   - **Type**
   - **Description**: the problem or goal and why it matters
   - **Acceptance criteria**: a checklist
   - **Implementation plan**: concrete steps, with the files involved
   - **Out of scope / open questions**

4. **Record the issue** once the plan is approved:
   - **GitHub repo:** with `gh`, try `gh issue create --title <title> --body-file <file>` (write the body to a scratchpad file first; add a label only if it already exists in the repo). Without `gh`, use the GitHub MCP server's issue creation tool (e.g. `issue_write`/`create_issue`) with the same title and body. Report the issue number and URL. If both fail (not authenticated, no issues enabled, ...), fall back to a file and say why.
   - **Otherwise / fallback:** write `issues/<NNN>-<kebab-title>.md` in the repository root, where `NNN` is the next free zero-padded number in `issues/`. Start the file with front matter (`title`, `type`, `status: open`) followed by the same sections as the plan.

5. **Implement immediately after recording**, following the same steps as Work mode below (steps 3-4): implement the plan, verify it, commit and push under the user's own git identity, and open a PR (`Closes #<n>` in the body when it's a GitHub issue). **Stop once the PR is created** — do not merge it. Report the PR link and tell the user it's ready for their review and merge.

## Work mode

1. **Load the issue.** For a GitHub number use `gh issue view <n>`, or the GitHub MCP server's issue read tool (e.g. `issue_read`/`get_issue`) when `gh` is unavailable; for a file, read it. Use the issue number (or the file path) as the reference for the whole task, e.g. in branch names (`feat/12-short-title`) and commit or PR messages (`Closes #12`).

2. **Check it is fully defined.** If the description, acceptance criteria or plan are missing or vague, switch to plan mode and refine it first (update the issue afterwards: `gh issue edit` or the MCP server's issue update tool, or edit the file).

3. **Implement** following the issue's plan and tick off acceptance criteria as they are met. Verify the result (tests, running the code) before declaring it done.

4. **Rebase onto latest main, then push and open a PR, then stop:**
   - Commit the work under the user's own git identity (see Git Identity in the global `CLAUDE.md`).
   - Update `main`: `git fetch origin main:main` (fails if `main` is checked out elsewhere, e.g. in another worktree — in that case `git -C <main-worktree-path> pull` instead). Then rebase the issue branch onto it: `git rebase main`. Resolve any conflicts before continuing.
   - Push the rebased branch (force-with-lease, since the rebase rewrites history): `git push --force-with-lease`.
   - **GitHub issue:** open a PR (`gh pr create`, or the MCP server's `create_pull_request` when `gh` is unavailable) whose body contains `Closes #<n>`, so the issue closes automatically when the user merges it. Do not merge the PR yourself.
   - **File issue:** open a PR as above, and set `status: in-review` in the front matter with a short note pointing to the PR. Once the user merges it, `status` can be set to `completed`.

5. **Report** the reference, what was implemented, and the PR link. Tell the user it's ready for their review and merge. If verification failed, leave the issue open and say so instead of opening a PR.
