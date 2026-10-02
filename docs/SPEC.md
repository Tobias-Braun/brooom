# Brooom product specification

Brooom is a fast, safe, cross-platform CLI tool that sweeps disk clutter
caused by modern AI-assisted development workflows: agent run logs and
runtime files, merged git branches, leftover worktrees, bloated git
histories, and build artifacts across many projects on one machine.

Its core is one command, `brooom sweep [preset] [path]`: presets choose what
is swept, one config file customizes it, and `--format` picks the output.
Everything else (`review`, `undo`, `sessions`, `empty-trash`, `config`) serves
that sweep.

The CLI is the first-class product. A dashboard and an agent layer are
secondary features that come later and build on the CLI's core.

## Problem

Heavy AI/agent usage makes disk load on a single dev machine grow fast:

- Agent tools (Claude Code, Cursor, Aider, Copilot, custom scripts) leave run
  logs, JSONL transcripts, caches and scratch files in project folders,
  dotfolders and well-known user-level locations.
- Parallel work leaves dozens of stale or already-merged branches and
  orphaned worktrees per repo.
- Long-lived repos accumulate loose objects, reflogs and packs.
- node_modules, dist, target, .venv, caches and similar pile up in projects
  that are no longer active.

Existing tools (npkill, git-sweep, ncdu, git-worktree helpers) each cover one
slice. Brooom unifies them, adds agent-tool-specific detectors, and makes
cleanup safe, fast and reviewable.

## Core principles (non-negotiable)

1. **Safety first.** Every action shows its plan and asks once
   (`Proceed? [y/N]`) before it changes anything; only an explicit yes acts.
   `--dry-run` stops after the plan, `-y`/`--yes` skips the question for
   scripted use, and without a terminal an unanswered question is an error.
   Nothing is ever deleted silently, and `sweep` never removes unmerged or
   uncommitted work.
2. **Scoped by default.** Without a path, Brooom operates only on the current
   git repo (detected by walking up to the nearest `.git`); run from a linked
   worktree, that is the whole repository with all its worktrees. A folder
   given as the path argument (`brooom sweep tidy ~/code`) is walked
   recursively and every repo and project folder inside it is covered; a
   filesystem root is refused. Every path is symlink-resolved and validated to
   lie inside the current repo or the given folder; anything outside is
   refused.
3. **Detectors find, actions act.** Detectors never modify anything and
   produce structured findings. Actions consume findings, are individually
   configurable, and always have a dry-run mode.
4. **Reversible by default.** Removed files go to the OS trash; branches are deleted with `git branch -d`; `-D` is used only
   when base ancestry or remote containment is re-verified at apply time
   (a squash/rebase merge counts only with the commits on a remote), or when
   `brooom review` was told to delete an unmerged branch; worktrees are
   moved to the trash and deregistered from git (a plain `git worktree remove`
   would permanently delete ignored files); git history pruning uses a
   conservative expiry. Every applied session writes a manifest so
   `brooom undo` can restore what is restorable.
5. **Fast.** Parallel directory walking, skip lists for known-huge irrelevant
   dirs, cached scan results with invalidation by mtime. Brooom should feel
   instant on a machine with hundreds of repos.

## Platforms and distribution

- Go, single static binary, no runtime dependencies. Windows, macOS and
  Linux (amd64 + arm64). Handle path separators, case-insensitive
  filesystems, Windows file locking and the three OS trash implementations
  (Recycle Bin, macOS Trash via a Finder-compatible mechanism, freedesktop
  trash spec on Linux) behind one interface.
- Windows Recycle Bin limits: the shell API behind it does not accept
  `\\?\` long-path prefixes, so paths longer than 259 UTF-16 characters are
  refused. The same refusal applies when the bin is disabled or too small for
  an item, because Windows would delete such items permanently; brooom never
  lets that happen silently.
- Release track with GoReleaser + GitHub Actions: tagged releases build
  binaries for all platform/arch combinations, produce checksums and a
  changelog, and publish GitHub releases. Package manager publishing
  (Homebrew tap, Scoop, winget, AUR, .deb/.rpm, `go install`, curl|sh install
  script) is prepared but not enabled yet.
- A version command. Brooom never contacts the network on its own; the only
  network call is the optional `gh` lookup of open pull requests.

## Configuration

- One JSON config file, `~/.brooom/config.json`, with sensible defaults so
  the tool works with zero config inside a repo. Scan cache and session
  manifests live next to it (`cache/`, `sessions/`).
- Config covers: thresholds (age, size), detector toggles and settings,
  custom patterns and locations, output defaults and the default sweep
  preset. `brooom config path|init|show|edit` manage the file; every run
  validates it.
- Two layers of configurability, both first-class:
  - Intent presets: `brooom sweep [after-agents|tidy|everything]`
    (default `everything`).
  - Fine-grained control: every detector has its own config keys (branch
    age, merge detection mode, worktree handling, gc expiry, which artifact
    dirs, which tool locations); `--detector` narrows a preset.
- Optional per-repo `.brooom.json` that can tighten but never loosen global
  safety rules. Minimal in v1.

## Removal

- Files and worktrees go to the OS trash, and every session can be undone.
- `brooom empty-trash` deletes what brooom put in the OS trash permanently;
  nothing else in the trash is touched.

Branches are deleted with `git branch -d`; `-D` is used only when base ancestry
or remote containment is re-verified at apply time, or when `brooom review`
was told to delete an unmerged branch; a merge found only by squash/rebase
detection counts when the commits are also on a remote. It is never used for
protected, base or checked-out branches.

Branch deletion is recoverable for a limited time only. Git deletes a branch's
own reflog together with the branch, so the reflog is no safety net. Brooom
records the tip commit of every deleted branch in the session manifest,
`brooom undo` recreates the branch there, and the output prints the manual
recovery command: `git branch <name> <sha>`, run inside the repository. The
commits remain only as unreachable objects and may be pruned by the next
`git gc` after `gc.pruneExpire` (default 2 weeks), so recover promptly.

## Output formats

`brooom sweep` reports findings in every format; with `--dry-run`, or with a
machine format (`json`, `ndjson`, `plain`), it prints the report and changes
nothing:

- `table` (default, human-readable, grouped by detector, sizes humanized,
  totals per group and overall reclaimable space)
- `tree` (findings shown in their directory structure)
- `json` (full findings schema, machine-readable, stable for scripting)
- `ndjson` (one finding per line, for streaming/piping)
- `plain` (paths only, one per line, for xargs-style use)
- `summary` (just counts and reclaimable bytes per detector)

Commands that list something other than findings support the formats that
make sense for their rows: `sessions` takes `table` (default), `plain`,
`json` and `ndjson`; `config show` takes `json` and `table`; `version` takes
`table`, `plain` and `json`. `tree` and `summary` describe findings only.
`--format` and `--detector` are rejected (usage error, exit 2) on commands
that would ignore them: `undo`, `review` and `empty-trash` print a plan or
questions as text, and `config init`, `config edit` and `config path` report a
result line. `completion` and `help` accept every global flag, because the
shell hands them the flags of the words it completes.

Respect `NO_COLOR`, detect TTY vs pipe, and support `--quiet`.

## Findings model

See [findings.md](findings.md). Every detector emits findings with a common
schema: id, detector, scope, path, kind, size_bytes, last_modified, age_days,
confidence, evidence, suggested_action, risk_flags. Findings are the contract
between detectors, output formats, actions and the future dashboard/agent.

## Detectors (v1)

Git:

- **stale-branch**: last commit age, remote tracking gone or never pushed,
  no open PR (via `gh` if present, optional). Unmerged work, so no preset
  runs it; `brooom review` does.
- **merged-branch**: tip is ancestor of main/master/origin/HEAD, or
  squash-merge detected via patch-id / `git cherry` against the base branch.
  Merge detection mode configurable (ancestor-only, ancestor+squash).
- **worktrees**: worktrees whose branch is merged (not merely freshly
  created) or stale, or whose branch ref or upstream is gone (the missing ref
  is reported without suggested action), worktrees
  whose directory is missing (prunable). Detached worktrees whose commits
  all landed on the base under other ids (rebased or squashed, patch-id
  detection) count as merged. There is no age threshold for removal: a
  cleanup right after a large agent run removes the fresh clean merged
  worktrees at once (`recently_modified` is informational only). Worktrees in
  use are protected, dirty worktrees are left to `brooom review`, and removed
  worktrees go to the trash and stay undoable.
- **git-bloat**: loose object count, reflog size, pack count, large blobs;
  suggests `git gc`, `git prune`, reflog expiry, with configurable expiry
  dates (run by the `everything` preset).

Files:

- **ai-artifacts**: known agent artifact locations and patterns, both
  project-level (`.claude/`, `.cursor/`, `.aider*`, `.copilot/`, run
  `*.jsonl`, agent scratch dirs) and the data a tool keeps per repository in
  the home directory (Claude Code's `~/.claude/projects/<repository>`
  transcripts, including those of the repository's agent worktrees). Global
  caches that belong to no repository are not cleaned. This is the
  differentiator; the list is maintained as data (embedded JSON),
  extensible via config, with docs on how to contribute new tool entries.
  Configuration files of these tools (settings, instructions, skills,
  commands) are never clutter.
- **log-and-runtime-files**: well-known names and locations of general dev
  tooling: npm/yarn/pnpm debug logs and caches, pip/poetry/uv caches,
  Docker/testcontainers leftovers, Jest/Vitest/pytest caches, coverage dirs,
  `.DS_Store`, `Thumbs.db`, editor swap files, crash dumps, rotated logs.
  Files currently open by a process are flagged instead of suggested.
- **build-artifacts**: node_modules, dist, build, out, target, .venv,
  `__pycache__`, .next, .nuxt, .turbo, .gradle, etc., weighted by project
  inactivity (last commit / last source mtime).

Thresholds and pattern lists are configurable globally and per detector, and
a repository's `.brooom.json` may tighten them. The global `thresholds.min_age_days` filters ai-artifacts,
log-and-runtime-files, stale-branch, worktrees and merged-branch (the last
three only once it is raised above the built-in default, see
[config.md](config.md)); `thresholds.min_size_bytes` filters ai-artifacts,
log-and-runtime-files and build-artifacts. git-bloat has
neither, and build-artifacts weighs project inactivity instead of age.

## Actions (v1)

trash (OS trash), delete-branch (`-d`, `-D` only as described above),
remove-worktree (+ prune), git-gc/prune/reflog-expire (with expiry),
restore/undo (from session manifest). All actions: plan first, one confirmation, `--dry-run` to stop
after the plan, `--yes` to skip the question, summary of reclaimed space at
the end, manifest written per session.

## CLI surface

```
brooom sweep [PRESET] [PATH] [--dry-run] [-y] [--format F] [--detector X]
brooom review [PATH] [--dry-run]                # decide on dirty and unmerged work
brooom undo [session-id] [--path P] [--dry-run] [-y] # default: latest session
brooom sessions                                 # id, repository, items, reclaimed
brooom empty-trash [--dry-run] [-y]             # delete brooom's items from the OS trash
brooom config path|init|show|edit
brooom version
```

`brooom undo` lists, last applied first, what it would restore and what it
cannot (maintenance actions, failed or skipped entries, a missing stored
copy, an unknown action, an entry outside the current scope, an entry an
earlier release removed with its quarantine or delete strategy),
each with the reason and the manual recovery hint. Nothing is ever
overwritten: an existing original path or branch is reported as a conflict.
It then asks `Restore N items? [y/N]` (unless `--yes` or `--dry-run`; without a terminal and
without `--yes` it exits 2), saves the manifest after every entry and exits 1
if a restorable entry conflicted or failed. Entries are only restored inside
the current scope (the repository you are in, or `--path`), because
manifests are editable files. Running it again skips restored entries.
`brooom empty-trash` lists the items the session manifests say
brooom moved there and that are still there, asks once and deletes them
permanently. Nothing else in the trash is touched: an item is only deleted
when its stored copy lies inside an OS trash directory and still has the
recorded type (and, for a file, size); its manifest entry becomes not
restorable.

Shell completions (bash, zsh, fish, PowerShell) and good `--help` text.

## Secondary features (after the CLI is solid)

- Dashboard: `brooom serve` starts a localhost API + embedded Vue 3 /
  TypeScript UI: treemap of usage per root with findings overlaid,
  per-detector tables with bulk selection, a review queue for
  approve/reject, session history with undo.
- Agent layer: the agent receives findings JSON plus repo metadata and gets
  read-only tools (inspect_branch, git_log, read_file_head, list_dir) and
  propose_action. It never executes. Anthropic API and OpenAI-compatible
  local endpoint; documents what data leaves the machine.

Both must be pure clients of the same core and API the CLI uses.

## Landing page

Astro site with Vue islands (`/site`, published as a container image to
ghcr.io and deployed by the maintainer's infrastructure). Astro
renders the static content; Vue components only for the interactive parts
(preset tabs, animated terminal demo of `brooom sweep`, install command
tabs per package manager, copy-to-clipboard). Theme: Brooom as a fast broom that sweeps your
workspace clean super fast so your agent fleet can run again. Dark
background, violet-to-blue gradients, slightly neon accents, developer
audience. Content: one-line pitch, one tab per sweep preset (its command and
exactly what it sweeps), animated terminal demo, install commands with tabs,
feature grid (safe by default, git-aware, agent-artifact-aware, fast,
cross-platform), one card per detector, config example, GitHub link. Minimal JS payload — hydrate only the islands that need it
(`client:visible` / `client:idle`).

## v1 scope, in order

1. Config loading from `~/.brooom`, repo detection, root validation with
   tests for path-escape and symlink cases across OSes.
2. Findings schema (Go types + JSON example) and output formats.
3. Git detectors (stale, merged, worktrees) and branch/worktree actions.
4. Trash abstraction for all three OSes, session manifests, undo.
5. ai-artifacts and log-and-runtime-files detectors with the embedded
   tool-location list.
6. build-artifacts and git-bloat.
7. `sweep` presets, completions, GoReleaser pipeline, package manager
   publishing (prepared, not enabled).
8. Landing page.
