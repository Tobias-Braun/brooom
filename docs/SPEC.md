# Brooom product specification

Brooom is a fast, safe, cross-platform CLI tool that sweeps disk clutter
caused by modern AI-assisted development workflows: agent run logs and
runtime files, stale/merged git branches, leftover worktrees, bloated git
histories, and build artifacts across many projects on one machine.

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

1. **Safety first.** Dry-run is the default for every action. Executing
   requires `--apply` plus per-item or per-group confirmation; `-y`/`--yes`
   skips the confirmation for scripted use. Nothing is ever deleted silently.
2. **Scoped by default.** With no flags, Brooom operates only on the current
   git repo (detected by walking up to the nearest `.git`). `--workspaces`
   runs over all configured workspace roots, walking the tree recursively and
   finding every repo and project folder inside them. Every path is
   symlink-resolved and validated to lie inside the current repo or a
   configured root; anything outside is refused.
3. **Detectors find, actions act.** Detectors never modify anything and
   produce structured findings. Actions consume findings, are individually
   configurable, and always have a dry-run mode.
4. **Reversible by default.** Removed files go to a configurable trash
   strategy; branches use `git branch -d` unless `--force`; worktrees are
   removed with `git worktree remove` and pruned; git history pruning uses a
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
- Release track with GoReleaser + GitHub Actions: tagged releases build
  binaries for all platform/arch combinations, produce checksums and a
  changelog, and publish GitHub releases. Package manager publishing
  (Homebrew tap, Scoop, winget, AUR, .deb/.rpm, `go install`, curl|sh install
  script) is prepared but not enabled yet.
- A version command and an opt-in update-check command.

## Configuration

- JSON config in `~/.brooom/config.json`, with sensible defaults so the tool
  works with zero config inside a repo. Scan cache, session manifests and
  quarantine live there too (`cache/`, `sessions/`, `quarantine/`).
- Config covers: workspace roots, thresholds (age, size) globally and per
  root, detector toggles, custom patterns and locations, trash strategy,
  output defaults, agent settings.
- Two layers of configurability, both first-class:
  - No-brainer presets: `brooom sweep` (safe default set),
    `--preset safe|standard|aggressive`.
  - Fine-grained control: every detector and action has its own flags and
    config keys (branch age, merge detection mode, worktree handling, gc
    expiry, which artifact dirs, which tool locations).
- Optional per-repo `.brooom.json` that can tighten but never loosen global
  safety rules. Minimal in v1.

## Trash strategies (configurable globally and per action)

- `trash`: OS trash (default).
- `quarantine`: move into `~/.brooom/quarantine/<session-id>/` with a
  manifest; auto-purge after a configurable retention (e.g. 14 days) via
  `brooom purge` or on next run with a notice.
- `delete`: immediate permanent deletion (requires explicit config or flag,
  and a warning on first use).

Branch deletion is always recoverable via reflog for the reflog expiry
window; this is documented and the recovery command is surfaced in output.

## Output formats

Every listing/dry-run command supports `--format`:

- `table` (default, human-readable, grouped by detector, sizes humanized,
  totals per group and overall reclaimable space)
- `tree` (findings shown in their directory structure)
- `json` (full findings schema, machine-readable, stable for scripting)
- `ndjson` (one finding per line, for streaming/piping)
- `plain` (paths only, one per line, for xargs-style use)
- `summary` (just counts and reclaimable bytes per detector)

Respect `NO_COLOR`, detect TTY vs pipe, and support `--quiet`.

## Findings model

See [findings.md](findings.md). Every detector emits findings with a common
schema: id, detector, scope, path, kind, size_bytes, last_modified, age_days,
confidence, evidence, suggested_action, risk_flags. Findings are the contract
between detectors, output formats, actions and the future dashboard/agent.

## Detectors (v1)

Git:

- **stale-branch**: last commit age, remote tracking gone or never pushed,
  no open PR (via `gh` if present, optional).
- **merged-branch**: tip is ancestor of main/master/origin/HEAD, or
  squash-merge detected via patch-id / `git cherry` against the base branch.
  Merge detection mode configurable (ancestor-only, ancestor+squash).
- **worktrees**: worktrees whose branch is merged/stale/deleted, worktrees
  whose directory is missing (prunable), dirty worktrees flagged not
  suggested.
- **git-bloat**: loose object count, reflog size, pack count, large blobs;
  suggests `git gc`, `git prune`, reflog expiry, with configurable expiry
  dates, exposed as separate "purge" options with clear explanations.
- **large-untracked / ignored-bloat**: large untracked or ignored files
  inside repos.

Files:

- **ai-artifacts**: known agent artifact locations and patterns, both
  project-level (`.claude/`, `.cursor/`, `.aider*`, `.copilot/`, run
  `*.jsonl`, agent scratch dirs) and user-level well-known locations (tool
  caches and logs under `~/.cache`, `~/Library/Logs`, `%LOCALAPPDATA%`). This
  is the differentiator; the list is maintained as data (embedded JSON),
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

Thresholds and pattern lists are configurable globally, per root, per
detector.

## Actions (v1)

trash (per trash strategy), delete-branch (`-d`, `--force` for `-D`),
remove-worktree (+ prune), git-gc/prune/reflog-expire (each opt-in with
expiry), restore/undo (from session manifest), purge (empty quarantine past
retention). All actions: dry-run default, `--apply` to execute, `--yes` to
skip confirmation, summary of reclaimed space at the end, manifest written
per session.

## CLI surface

```
brooom                          # scan current repo, table output, dry-run
brooom scan [--workspaces] [--detector X] [--format F]
brooom sweep [--preset P] [--apply] [-y]   # the no-brainer command
brooom branches [--stale] [--merged] [--apply]
brooom worktrees [--apply]
brooom git purge [--gc] [--reflog-expire D] [--prune D] [--apply]
brooom logs / brooom artifacts / brooom ai
brooom clean --from findings.json [--apply] [-y]
brooom undo [session-id] / brooom sessions / brooom purge
brooom roots add|remove|list
brooom config init|show|edit|validate
brooom version / brooom update-check
```

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

Astro site with Vue islands (`/site`, deployed via GitHub Pages). Astro
renders the static content; Vue components only for the interactive parts
(animated terminal demo of `brooom sweep`, install command tabs per package
manager, copy-to-clipboard). Theme: Brooom as a fast broom that sweeps your
workspace clean super fast so your agent fleet can run again. Dark
background, violet-to-blue gradients, slightly neon accents, developer
audience. Content: one-line pitch, animated terminal demo, install commands
with tabs, feature grid (safe by default, git-aware, agent-artifact-aware,
fast, cross-platform), a "what it finds" section, config example, GitHub
link. Minimal JS payload — hydrate only the islands that need it
(`client:visible` / `client:idle`).

## v1 scope, in order

1. Config loading from `~/.brooom`, repo detection, root validation with
   tests for path-escape and symlink cases across OSes.
2. Findings schema (Go types + JSON example) and output formats.
3. Git detectors (stale, merged, worktrees) and branch/worktree actions.
4. Trash abstraction for all three OSes with the three strategies, session
   manifests, undo.
5. ai-artifacts and log-and-runtime-files detectors with the embedded
   tool-location list.
6. build-artifacts and git-bloat/purge.
7. `sweep` presets, completions, GoReleaser pipeline, package manager
   publishing (prepared, not enabled).
8. Landing page.
