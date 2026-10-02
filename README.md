<div align="center">
  <img src=".github/assets/logo.svg" alt="Brooom logo" width="96">
  <h1>Brooom</h1>
  <p><strong>Sweep your workspace clean, fast - so your agent fleet can run again.</strong></p>

[![CI](https://github.com/Tobias-Braun/brooom/actions/workflows/ci.yml/badge.svg)](https://github.com/Tobias-Braun/brooom/actions/workflows/ci.yml)
[![Release](https://github.com/Tobias-Braun/brooom/actions/workflows/release.yml/badge.svg)](https://github.com/Tobias-Braun/brooom/actions/workflows/release.yml)
[![Release check](https://github.com/Tobias-Braun/brooom/actions/workflows/release-check.yml/badge.svg)](https://github.com/Tobias-Braun/brooom/actions/workflows/release-check.yml)
[![Site](https://github.com/Tobias-Braun/brooom/actions/workflows/site.yml/badge.svg)](https://github.com/Tobias-Braun/brooom/actions/workflows/site.yml)
[![Repo sync](https://github.com/Tobias-Braun/brooom/actions/workflows/repo-sync.yml/badge.svg)](https://github.com/Tobias-Braun/brooom/actions/workflows/repo-sync.yml)
[![License: MIT](https://img.shields.io/badge/license-MIT-a78bfa)](LICENSE)
[![Go 1.24](https://img.shields.io/badge/go-1.24-60a5fa)](go.mod)

</div>

## About

> Brooom - sounds like a fast car or a sweeper - and thats exactly what it is!

Brooom finds and cleans the disk clutter that agent tools, parallel branches
and forgotten projects leave behind. It is a fast, safe, cross-platform CLI,
and nothing is changed before you have seen the plan and said yes. It sweeps:

- **Agent artifacts** — run logs, JSONL transcripts, caches and scratch files
  from Claude Code, Cursor, Aider, Copilot and friends
- **Git leftovers** — already-merged branches (squash merges too), orphaned
  worktrees, bloated histories
- **Build artifacts** — `node_modules`, `dist`, `target`, `.venv` and co. in
  projects you stopped touching
- **Logs and runtime junk** — debug logs, test caches, coverage output,
  `.DS_Store`, crash dumps

> Status: under active development towards the first major release.

## Safe by default

- Every command that changes something **shows its plan and asks** once
  before it acts (`--dry-run` only shows the plan, `--yes` skips the question
  in scripts). Without an interactive terminal it refuses instead of guessing.
- `sweep` never removes unmerged or uncommitted work.
- Files go to your **OS trash**; branches are deleted with `git branch -d`;
  worktrees are moved to the trash and then deregistered from git. Nothing is
  deleted permanently until you run `br empty-trash`.
  Linked worktrees outside the scanned repository (such as `../repo-wt`) are
  never touched; pass the folder that holds them as the path to include them.
  On Windows the Recycle Bin cannot take paths longer than 259 characters
  (the shell API rejects `\\?\` paths) or items larger than the bin limit;
  Brooom leaves those alone instead of letting Windows delete them
  permanently.
- Trashing a Windows junction is refused (a junction is a name-surrogate
  link, and its target is never followed). Directories holding version
  control metadata (`.git`, `.hg`, `.jj`, `.svn`) are never removed.
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
network call is `gh` asking GitHub for open pull requests while merged
branches are checked; set `"git": {"use_gh": false}` in the config for a fully
offline run.

## Usage

```sh
br sweep                # show everything worth cleaning, ask once, then clean
br sweep after-agents   # merged worktrees and branches, agent leftovers
br sweep tidy           # logs, OS junk, test caches, coverage output
br sweep tidy ~/code    # every repository below ~/code
br sweep --dry-run      # only show what it would do
br sweep --yes          # no question (scripts)
br review               # decide one by one on dirty worktrees and unmerged branches
br undo                 # show what the last session removed, ask, restore
                        # (a sweep of a folder prints `br undo <id> --path <folder>`)
br sessions             # list past sessions: id, repository, items, reclaimed
br empty-trash          # permanently delete what brooom put in the OS trash
br config edit          # customize everything in one config file
```

Every command, flag and example is listed in the [CLI reference](docs/cli.md)
(generated from the command tree). Shell completions for bash, zsh, fish and
PowerShell come with the binary, for example `source <(brooom completion bash)`;
`brooom completion --help` shows how to install them for each shell. On Linux,
bash completion installs per user with
`brooom completion bash > ~/.local/share/bash-completion/completions/brooom`
(the system path `/etc/bash_completion.d` needs `sudo`).

### Live progress

While `sweep`, `review` and `undo` run, stderr shows a live display: the phase (discover, scan, plan, apply), a
spinner and progress bar, finding counts per detector and per target, and the
bytes reclaimed so far. When the command ends it collapses to one summary line;
the results (table, tree, summary) stay on stdout, and the display steps aside
for confirmation prompts.

It draws only when stderr is a terminal, the format is `table`, `tree` or
`summary`, and neither `--quiet`, `CI` nor `TERM=dumb` is in effect.
`NO_COLOR` and `--no-color` only remove the colours. The machine formats
(`json`, `ndjson`, `plain`) never show it, so scripts and AI agents get
exactly the same stdout on a terminal and in a pipe.

### Sweep presets

`brooom sweep [preset]` scans with a fixed detector set, shows the plan, asks
`Proceed? [y/N/e to choose]` once and then cleans. Only an explicit yes acts;
`e` opens a checklist of every item (all ticked) to untick what should stay;
`--yes` skips the question and `--dry-run` stops after the plan. Afterwards it prints
what it removed and how much disk that reclaimed (`2 worktrees deleted, 5
merged branches removed. 4.2 GB reclaimed`), and `brooom undo` restores. Without a preset, `sweep.preset` in the
config decides, else `everything`.

Sweep never removes unmerged or uncommitted work: dirty worktrees, branches
that are not merged and other findings with blocking risk flags are listed as
skipped, and sweep has no `--force`. `brooom review` walks through exactly
that work, one item at a time: it shows the changed and untracked files, the
commits that exist on no remote and the last activity, and asks `[d]elete /
[k]eep / [q]uit`; deletions are one session for `brooom undo`, and q discards
every choice. `.brooom.json` can still tighten what a preset selects, and
`--detector` narrows it. The
definitions live in `internal/presets`; `brooom sweep --help` prints them.

| Preset                 | Detectors                                        | Min. confidence              | Notes                                                                                                                                             |
| ---------------------- | ------------------------------------------------ | ---------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------- |
| `after-agents`         | worktrees, merged-branch, ai-artifacts           | medium                       | clean up after an agent run: merged (also squash and rebase merged) clean worktrees and branches, AI tool artifacts in the repository             |
| `tidy`                 | log-and-runtime-files                            | medium                       | debug and rotated logs, OS junk, test caches, coverage output                                                                                     |
| `everything` (default) | after-agents + tidy + build-artifacts, git-bloat | medium, build artifacts high | build artifacts of inactive projects only, gc/reflog expire/prune (a configured expiry is only ever shortened to `90.days.ago`, never lengthened) |

The preset names of earlier releases (`safe`, `standard`, `aggressive`) still
work and run `everything`, with a note saying so.

### Output formats

`--format` (or `output.format` in the config) picks `table` (default),
`tree`, `summary`, `json`, `ndjson` or `plain`. With `--dry-run`, or with a
machine format (`json`, `ndjson`, `plain`), sweep prints the report in that
format and changes nothing: `br sweep -f json > findings.json` is the
read-only report for scripts. `--format plain` is a bare path list for
pipes and omits informational findings.

### Configuration

One file, `~/.brooom/config.json` (`br config path` prints where it is,
`--config` picks another one), customizes every detector, threshold, the
default preset and the output. `br config init` writes the defaults,
`br config show` prints the effective configuration and `br config edit`
opens it and checks it afterwards. A repository's `.brooom.json` may only
tighten the rules for that repository. See the
[configuration reference](docs/config.md).

## Documentation

- [Product specification](docs/SPEC.md)
- [Architecture](docs/ARCHITECTURE.md)
- [Configuration](docs/config.md)
- [Findings schema](docs/findings.md)
- [Releasing](docs/releasing.md)
- [Tool catalog](docs/catalog.md)
- [Build artifacts](docs/build-artifacts.md)
- [Contributing](CONTRIBUTING.md)

## License

[MIT](LICENSE)
