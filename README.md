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

## Install

Download an archive for your platform from the
[GitHub releases](https://github.com/Tobias-Braun/brooom/releases) or use the
install script (verifies the sha256 checksum, installs to `~/.local/bin`, never
uses sudo):

```sh
# Linux and macOS
curl -fsSL https://raw.githubusercontent.com/Tobias-Braun/brooom/main/scripts/install.sh | sh
```

```powershell
# Windows (PowerShell)
irm https://raw.githubusercontent.com/Tobias-Braun/brooom/main/scripts/install.ps1 | iex
```

With a Go toolchain:

```sh
go install github.com/Tobias-Braun/brooom/cmd/brooom@latest
```

Coming soon: Homebrew, Scoop, winget, AUR and deb/rpm packages. See
[releasing](docs/releasing.md) for the release process.

## Privacy

Brooom has no telemetry and never contacts the network on its own. The only
network access in the whole tool is the opt-in update check:

- `brooom update-check` sends one unauthenticated `GET` to
  `https://api.github.com/repos/Tobias-Braun/brooom/releases/latest`
  (headers `Accept` and `User-Agent: brooom/<version>` only, no token, no
  query parameters, no data about you) and prints whether a newer release
  exists and how to upgrade. Running the command is your consent. Brooom
  never updates itself.
- Optionally set `"update_check": true` in `~/.brooom/config.json` to let
  Brooom check at most once per 24 hours in the background. The result is
  cached in `~/.brooom/cache/update.json`, and a one-line hint goes to
  stderr after a command when a newer version exists. It only runs in an
  interactive terminal with the default table output, never with `--quiet`,
  and never delays or fails a command.
- Set `BROOOM_NO_UPDATE_CHECK=1` to disable the background check regardless
  of the config. `BROOOM_UPDATE_URL` points the check at a mirror (mainly
  used by tests).

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

- [Product specification](docs/SPEC.md)
- [Architecture](docs/ARCHITECTURE.md)
- [Findings schema](docs/findings.md)
- [Releasing](docs/releasing.md)
- [Contributing](CONTRIBUTING.md)

## License

[MIT](LICENSE)
