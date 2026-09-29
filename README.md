# Brooom

**Sweep your workspace clean, fast — so your agent fleet can run again.**

Brooom is a fast, safe, cross-platform CLI that finds and cleans the disk
clutter modern AI-assisted development leaves behind:

- **Agent artifacts** — run logs, JSONL transcripts, caches and scratch files
  from Claude Code, Cursor, Aider, Copilot and friends
- **Git leftovers** — stale and already-merged branches (squash merges too),
  orphaned worktrees, bloated histories
- **Build artifacts** — `node_modules`, `dist`, `target`, `.venv` and co. in
  projects you stopped touching
- **Logs and runtime junk** — debug logs, test caches, coverage output,
  `.DS_Store`, crash dumps

> Status: under active development towards v0.1.0.

## Safe by default

- Every command is a **dry run** unless you pass `--apply`.
- Applying asks for confirmation (skip with `--yes` in scripts).
- Files go to your **OS trash** (or a quarantine folder); branches are
  deleted with `git branch -d`; worktrees with `git worktree remove`.
- Every applied session is recorded and can be reverted with `brooom undo`.
- Without flags Brooom only touches the repository you are in; paths outside
  the allowed scope are refused, symlinks are never followed out of it.

## Usage

```sh
brooom                      # scan the current repo, dry run
brooom sweep                # the no-brainer: safe preset, dry run
brooom sweep --apply        # ...and clean up (asks first)
brooom branches --merged    # merged branches, incl. squash merges
brooom worktrees --apply    # remove leftover worktrees
brooom ai --user            # agent artifacts, incl. user-level caches
brooom scan -w -f json      # all workspace roots, machine-readable
brooom undo                 # restore the last session
```

Output formats: `table` (default), `tree`, `json`, `ndjson`, `plain`,
`summary`.

## Documentation

- [Architecture](docs/ARCHITECTURE.md)
- [Findings schema](docs/findings.md)
- [Contributing](CONTRIBUTING.md)

## License

[MIT](LICENSE)
