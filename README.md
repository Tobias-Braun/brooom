<div align="center">
  <img src=".github/assets/logo.svg" alt="Brooom logo" width="96">
  <h1>Brooom</h1>
  <p><strong>Sweep your workspace clean, fast, so your agent fleet can run again.</strong></p>

  [![CI](https://github.com/Tobias-Braun/brooom/actions/workflows/ci.yml/badge.svg)](https://github.com/Tobias-Braun/brooom/actions/workflows/ci.yml)
  [![Release](https://github.com/Tobias-Braun/brooom/actions/workflows/release.yml/badge.svg)](https://github.com/Tobias-Braun/brooom/actions/workflows/release.yml)
  [![Release check](https://github.com/Tobias-Braun/brooom/actions/workflows/release-check.yml/badge.svg)](https://github.com/Tobias-Braun/brooom/actions/workflows/release-check.yml)
  [![Site](https://github.com/Tobias-Braun/brooom/actions/workflows/site.yml/badge.svg)](https://github.com/Tobias-Braun/brooom/actions/workflows/site.yml)
  [![Repo sync](https://github.com/Tobias-Braun/brooom/actions/workflows/repo-sync.yml/badge.svg)](https://github.com/Tobias-Braun/brooom/actions/workflows/repo-sync.yml)
  [![License: MIT](https://img.shields.io/badge/license-MIT-a78bfa)](LICENSE)
  [![Go 1.24](https://img.shields.io/badge/go-1.24-60a5fa)](go.mod)
</div>

## About

Brooom finds and cleans the disk clutter that agent tools, parallel branches
and forgotten projects leave behind. It is a fast, safe, cross-platform CLI,
and nothing is changed before you have seen the plan and said yes. It sweeps:

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

- Every command that changes something **shows its plan and asks** once
  before it acts (`--dry-run` only shows the plan, `--yes` skips the question
  in scripts). Without a terminal it refuses instead of guessing.
- `sweep` never removes unmerged or uncommitted work.
- Files go to your **OS trash** (or a quarantine folder); branches are
  deleted with `git branch -d`; worktrees are moved to the trash and then deregistered from git.
  Linked worktrees outside the scanned repository (such as `../repo-wt`) are
  never touched; `br scan` lists them with a hint to pass the folder that
  holds them as the path (not in `--format plain`, which stays a bare path
  list).
  On Windows the Recycle Bin cannot take paths longer than 259 characters
  (the shell API rejects `\\?\` paths) or items larger than the bin limit;
  Brooom refuses those instead of letting Windows delete them permanently and
  points to `--trash-strategy quarantine`.
- The `delete` strategy (permanent removal) is refused outside a git
  repository and whenever git cannot show right now that the path holds no
  untracked, non-ignored file. Trashing a Windows junction is refused too
  (a junction is a name-surrogate link, and its target is never followed).
  Directories holding version control metadata (`.git`, `.hg`, `.jj`, `.svn`)
  are never removed.
- Every applied session is recorded and can be reverted with `br undo`.
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

The installer does not change your PATH by default. `iex` cannot pass a switch,
so to add the install directory to your user PATH run the script through a
scriptblock, or set `BROOOM_ADD_TO_PATH=1` first:

```powershell
& ([scriptblock]::Create((irm https://raw.githubusercontent.com/Tobias-Braun/brooom/main/scripts/install.ps1))) -AddToPath
```

Upgrading while brooom is running works: the old `brooom.exe` is renamed to
`brooom.exe.old` and removed on the next run.

Both install scripts also add `br`, a short command for `brooom` (a symlink,
or a hardlinked `br.exe` on Windows), as long as nothing else uses `br`. If
`br` is taken, for example by [broot](https://github.com/Canop/broot)'s shell
function, by an alias or by another binary, the installer leaves it alone and
tells you why. `brooom` always works, and every `br` example below works with
`brooom` too.

With a Go toolchain (installs `brooom` only, without `br`):

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
  never fails a command, and waits at most 200 ms for the answer before
  giving up (a slow or failed request is not retried for an hour).
- Set `BROOOM_NO_UPDATE_CHECK=1` to disable the background check regardless
  of the config. `BROOOM_UPDATE_URL` points the check at a mirror (mainly
  used by tests).

## Usage

```sh
br                      # scan the current repo and suggest what to sweep
br sweep                # show everything worth cleaning, ask once, then clean
br sweep after-agents   # merged worktrees and branches, agent leftovers
br sweep tidy           # logs, OS junk, test caches, coverage output
br sweep tidy ~/code    # every repository below ~/code
br sweep --dry-run      # only show what it would do
br sweep --yes          # no question (scripts)
br review               # decide one by one on dirty worktrees and unmerged branches
br undo                 # show what the last session removed, ask, restore
                        # (a sweep of a folder prints `br undo <id> --path <folder>`)
br empty-trash          # permanently delete what brooom put in the OS trash
br purge                # delete quarantined sessions past their retention
```

Every command, flag and example is listed in the [CLI reference](docs/cli.md)
(generated from the command tree). Shell completions for bash, zsh, fish and
PowerShell come with the binary, for example `source <(brooom completion bash)`;
`brooom completion --help` shows how to install them for each shell. On Linux,
bash completion installs per user with
`brooom completion bash > ~/.local/share/bash-completion/completions/brooom`
(the system path `/etc/bash_completion.d` needs `sudo`).

### Live progress

While `scan`, `sweep`, `clean`, `git purge` and `undo`
run, stderr shows a live display: the phase (discover, scan, plan, apply), a
spinner and progress bar, finding counts per detector and per target, and the
bytes reclaimed so far. When the command ends it collapses to one summary line;
the results (table, tree, summary) stay on stdout, and the display steps aside
for confirmation prompts.

`--progress=auto|always|never` (default `auto`) controls it. `auto` draws only
when stderr is a terminal, the format is `table`, `tree` or `summary`, and
neither `--quiet`, `--verbose`, `CI` nor `TERM=dumb` is in effect. `NO_COLOR`
and `--no-color` only remove the colours. The machine formats (`json`,
`ndjson`, `plain`) never show it, whatever `--progress` says, so scripts and AI
agents get exactly the same stdout as before; `--progress=never` turns it off
explicitly.

### Sweep presets

`brooom sweep [preset]` scans with a fixed detector set, shows the plan, asks
`Proceed? [y/N/e to choose]` once and then cleans. Only an explicit yes acts;
`e` opens a checklist of every item (all ticked) to untick what should stay;
`--yes` skips the question and `--dry-run` stops after the plan. Afterwards it prints
what it removed and how much disk that reclaimed (`2 worktrees deleted, 5
merged branches removed. 4.2 GB reclaimed`), `--verbose` prints the full
summary, and `brooom undo` restores. Without a preset, `sweep.preset` in the
config decides, else `everything`.

Sweep never removes unmerged or uncommitted work: dirty worktrees, branches
that are not merged and other findings with blocking risk flags are listed as
skipped, and sweep has no `--force`. `brooom review` walks through exactly
that work, one item at a time: it shows the changed and untracked files, the
commits that exist on no remote and the last activity, and asks `[d]elete /
[k]eep / [q]uit`; deletions are one session for `brooom undo`, and q discards
every choice. Large untracked files are in no preset; `brooom scan -d
large-untracked` lists them. `.brooom.json` can
still tighten what a preset selects, and `--detector` narrows it. The
definitions live in `internal/presets`; `brooom sweep --help` prints them.

| Preset | Detectors | Min. confidence | Notes |
| --- | --- | --- | --- |
| `after-agents` | worktrees, merged-branch, ai-artifacts | medium | clean up after an agent run: merged (also squash and rebase merged) clean worktrees and branches, AI tool artifacts in the repository |
| `tidy` | log-and-runtime-files | medium | debug and rotated logs, OS junk, test caches, coverage output |
| `everything` (default) | after-agents + tidy + build-artifacts, git-bloat | medium, build artifacts high | build artifacts of inactive projects only, gc/reflog expire/prune (a configured expiry is only ever shortened to `90.days.ago`, never lengthened) |

The preset names of earlier releases (`safe`, `standard`, `aggressive`) still
work and run `everything`, with a note saying so.

Output formats: `table` (default), `tree`, `json`, `ndjson`, `plain`,
`summary`.

## Documentation

- [Product specification](docs/SPEC.md)
- [Architecture](docs/ARCHITECTURE.md)
- [Findings schema](docs/findings.md)
- [Releasing](docs/releasing.md)
- [Tool catalog](docs/catalog.md)
- [Build artifacts](docs/build-artifacts.md)
- [Contributing](CONTRIBUTING.md)

## License

[MIT](LICENSE)
