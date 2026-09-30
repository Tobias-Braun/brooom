# The catalog

The catalog is the data that tells Brooom where AI tools and dev tooling leave
clutter (run logs, transcripts, caches, crash dumps, OS junk). It is embedded
JSON in `internal/catalog/data/`, extensible through the config, and read by
the `ai-artifacts` and `log-and-runtime-files` detectors (and later
`build-artifacts`). The configuration files of a tool (settings, instructions,
skills, commands) are never clutter: every tool lists them under `protect`.

The `internal/catalog` package is pure. It never modifies anything and touches
the filesystem only to check which user locations exist.

## File format

Tool files (`ai_tools.json`, `dev_tools.json`) look like this:

```json
{
  "schema_version": 1,
  "tools": [
    {
      "id": "claude-code",
      "name": "Claude Code",
      "category": "ai",
      "homepage": "https://www.anthropic.com/claude-code",
      "entries": [
        {
          "scope": "user",
          "patterns": ["~/.claude/todos"],
          "os": [],
          "kind": "file",
          "confidence": "medium",
          "min_age_days": 30,
          "description": "Per-session todo lists of finished sessions",
          "source": "https://docs.claude.com/en/docs/claude-code/settings"
        }
      ],
      "protect": [
        {
          "scope": "user",
          "patterns": ["~/.claude/settings.json", "~/.claude/skills"],
          "os": [],
          "reason": "Settings and skills are configuration, not clutter"
        }
      ]
    }
  ]
}
```

JSON is decoded strictly: unknown fields are an error, so a typo in a
contributed file fails CI. Only the files named in `toolFiles`
(`internal/catalog/load.go`) use this format; other files under `data/` (for
example `build_artifacts.json`) have their own schema and decoder and are
ignored by the tools loader.

| Field | Values |
| --- | --- |
| `id` | Unique kebab-case id across all files; used in config toggles. |
| `category` | `ai`, `logs`, `cache`, `os-junk`, `crash`, `build`. Toggled through `detectors.log-and-runtime-files.categories`. `crash` is separate from `logs` because dumps are diagnostics some users want to keep. |
| `homepage` | Optional http(s) URL. |
| `entries[].scope` | `project` (patterns relative to a project root) or `user`. |
| `entries[].patterns` | One or more globs, forward slashes only. |
| `entries[].os` | Optional filter of `linux`, `darwin`, `windows`; empty means all. |
| `entries[].kind` | `file` or `dir`. A `file` entry never matches a directory and vice versa (a directory named `core` is not a crash dump). `any` exists only for config extras. |
| `entries[].confidence` | `high`, `medium` or `low`. |
| `entries[].verify` | Optional content check a file match must pass before it is reported: `core-dump` (ELF of type ET_CORE or Mach-O MH_CORE) or `minidump` (`MDMP`, `PAGEDUMP` or `PAGEDU64`). A file that fails it (a script named `core`, a database export named `*.dmp`), a symlink or an unreadable file is dropped, never reported at lower confidence. |
| `entries[].min_age_days` | Optional. An explicit `0` means "no age requirement", unset means "use the detector default". |
| `entries[].description` | Required, one short sentence. |
| `entries[].source` | Optional in the format, required for contributions (see the checklist). |
| `protect[]` | `scope`, `patterns`, `os` as for entries, plus a required `reason`. |

## Pattern syntax

- `*` and `?` never cross `/`; `[abc]`, `[a-z]` and `[!x]` are character
  classes.
- `**` as a whole path segment matches zero or more segments (`a/**/b`
  matches `a/b` and `a/x/y/b`; a trailing `a/**` matches `a` and everything
  below it). `**` inside a segment (`a**b`) is invalid.
- There is no escape character; a trailing or doubled `/` and unbalanced `[`
  are invalid.
- Matching is case-insensitive on Windows and macOS, case-sensitive on Linux.

### Project patterns

Project patterns are gitignore-like:

- A pattern without `/` matches the base name at any depth (`npm-debug.log*`).
- A pattern containing `/` is anchored at the project root
  (`.claude/*.log`).
- A leading `**/` means any depth explicitly.

They must be relative, without `..`, drive letters or backslashes.

### User patterns

User patterns start with one of these variables (anything else is rejected):

| Variable | Linux | macOS | Windows |
| --- | --- | --- | --- |
| `~` | home | home | home (`USERPROFILE`) |
| `$XDG_CACHE_HOME` | value if absolute, else `~/.cache` | same | undefined, skipped |
| `$XDG_DATA_HOME` | value if absolute, else `~/.local/share` | same | undefined, skipped |
| `$XDG_CONFIG_HOME` | value if absolute, else `~/.config` | same | undefined, skipped |
| `~/Library/...` | skipped | home + `Library/...` | skipped |
| `%LOCALAPPDATA%` | undefined, skipped | undefined, skipped | `LOCALAPPDATA` |
| `%APPDATA%` | undefined, skipped | undefined, skipped | `APPDATA` |

A pattern whose variable is undefined on that OS is skipped silently. Write
patterns with forward slashes on every OS; expansion produces native paths. The
first directory below the variable must be literal (`~/.tool/*.log` is fine,
`~/*/x` is not) and `..` is rejected. `**` in a user pattern reaches at most
8 segments below `Base`.

## The `Base` contract

`Catalog.UserLocations(env, cats...)` expands user patterns for one OS and
returns only locations that exist and are readable. `PathEnv{GOOS, Home,
Getenv}` is injectable so tests can simulate any OS.

Each `UserLocation` has a `Base`: the literal directory prefix of the expanded
pattern up to the first wildcard segment (for wildcard-free patterns, the path
itself). `Base` is what gets added to the scope guard. **Detectors emit
findings only strictly below `Base`, never `Base` itself**, because an allowed
location itself can never be removed (`IsAllowedRoot`). For a wildcard-free
pattern such as `~/.claude/todos` they therefore report the entries directly
inside it, not `~/.claude/todos`. `UserLocation.Match(abs, isDir)` implements
exactly this rule, including the kind check and the depth bound.

`Catalog.UserProtection(env)` returns the expanded user-scope protect rules;
detectors must reject every candidate for which `Protected(abs)` is true.

## Protect rules

Protect always wins over entries, whatever their order, and applies whatever
categories a detector scans. `ProjectMatcher.Match` never matches a protected
path or anything below one; `Protected(rel)` is true when the path matches a
protect pattern or lies below a protected path.

Validation adds a self-consistency check: no entry may have a wildcard-free
pattern that equals or is an ancestor directory of a `protect` pattern of the
same tool and scope. An entry `~/.claude` next to the protect rule
`~/.claude/settings.json` fails to load. Use narrower entries such as
`~/.claude/todos`.

## Confidence and minimum age

| Kind of data | Confidence | `min_age_days` |
| --- | --- | --- |
| Session transcripts and history | `medium` | 30 |
| Caches and logs | `high` | 14 |
| OS junk recreated on demand | `high` | 0 |

Use `low` when a match might be something the user still wants.

## Extending through the config

`detectors.ai-artifacts.extra` and `detectors.log-and-runtime-files.extra`
accept the shorthand and the full format (see also [config.md](config.md)).

```json
{
  "detectors": {
    "log-and-runtime-files": {
      "extra": [
        { "id": "my-tool", "name": "My tool", "project": [".mytool/cache"], "user": ["~/.mytool/logs"] },
        {
          "id": "my-other-tool",
          "name": "My other tool",
          "category": "crash",
          "entries": [
            {
              "scope": "project",
              "patterns": ["**/*.dmp"],
              "kind": "file",
              "confidence": "high",
              "min_age_days": 7,
              "description": "Crash dumps of my other tool"
            }
          ],
          "protect": [
            { "scope": "project", "patterns": ["keep/*.dmp"], "reason": "Reference dumps" }
          ]
        }
      ]
    }
  }
}
```

- The shorthand `project`/`user` lists become entries of kind `any`
  (file or directory), confidence `medium`, described by `description` or
  the tool name. Entries in the full format default to kind `any` and
  confidence `medium` when those are omitted.
- An extra with a new id becomes a new tool. Its category defaults to `ai` for
  `ai-artifacts` and `logs` for `log-and-runtime-files`.
- An extra with the id of an embedded tool appends its entries and protect
  rules. The embedded name, category and homepage win, and extras can never
  remove or weaken protect rules.
- Extras are validated when the catalog loads, with every problem reported at
  once naming the extra, tool id and entry index.
- Toggle tools with `tools` (`{"cursor": false}`) and categories with
  `categories` (`{"os-junk": false}`); unlisted ones stay enabled. Entries have
  no `enabled` flag.

## Dev tooling entries (`dev_tools.json`)

Entries for the `log-and-runtime-files` detector. Plain `*.log` is deliberately
not an entry (applications keep meaningful logs); only named tool logs and
rotated `*.log.N` / `*.log.gz` files are. The first matching tool in id order
wins, so a name such as `yarn-debug.log.1` is reported once, as a rotated log
(medium), never twice.

Notes on the shape of the data:

- User cache entries are wildcard-free directories. By the `Base` contract
  above, detectors report the directories directly inside them (`http-v2`
  inside the pip cache), never the cache directory itself. Every one of them
  is rebuilt or re-downloaded on demand; the pnpm store, the npm `_cacache`
  and the Go build cache are `low` because clearing them costs a full
  re-download or rebuild (`pnpm store prune`, `npm cache clean --force` and
  `go clean -cache` are the official alternatives).
- `~/.npm/_logs/*.log` is `Base`-anchored at the log directory, so only the
  timestamped logs are found. On Windows the directory is
  `%LOCALAPPDATA%\npm-cache\_logs`.
- Crash reports below `~/Library/Logs/DiagnosticReports` match only named dev
  tool prefixes followed by a year (`node-2026-...`), never a blanket `*.ips`.
  Case-insensitive macOS matching is the reason `go*` is not used (it would
  match `Google Chrome`).
- `._*` (AppleDouble) is `low`: it is only clutter on non-macOS volumes
  (exFAT, FAT, network shares) and there is no schema field for sibling
  checks. `desktop.ini` is `low` because it can carry a user set folder icon.
- Swap, lock and auto-save files (`*.swp`, `*.swo`, `*~`, `.#*`, `#*#`) are
  `medium` with no minimum age. A running editor keeps them open; the scanner
  flags open files (issue #31), so they are not removed under a live session.
- Config files are protected so that they are never clutter, which also guards
  over-broad `extra` additions: `.npmrc`, `.yarnrc*`, `pytest.ini`,
  `.coveragerc`, `.eslintrc*`, `jest.config.*`, `.editorconfig` and
  `.gitignore`.

### Entries

| Tool | Patterns | Scope | OS | Category | Kind | Confidence | Min age | What regenerates it |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| npm | `npm-debug.log*` | project | all | logs | file | high | 14d | Debug logs npm leaves behind after a failed command; the next run writes new ones |
| npm | `~/.npm/_logs/*.log` | user | linux, darwin | logs | file | high | 14d | Timestamped npm debug logs in the npm cache directory; npm writes new ones on every run |
| npm | `%LOCALAPPDATA%/npm-cache/_logs/*.log` | user | windows | logs | file | high | 14d | Timestamped npm debug logs in the npm cache directory; npm writes new ones on every run |
| yarn | `yarn-error.log` | project | all | logs | file | high | 14d | Error log Yarn writes when a command fails; rewritten by the next failure |
| yarn | `yarn-debug.log*` | project | all | logs | file | high | 14d | Debug log Yarn writes when a command fails; rewritten by the next failure |
| pnpm | `pnpm-debug.log*` | project | all | logs | file | high | 14d | Debug log pnpm writes when a command fails; rewritten by the next failure |
| lerna | `lerna-debug.log*` | project | all | logs | file | high | 14d | Debug log Lerna writes when a command fails; rewritten by the next failure |
| jvm | `hs_err_pid*.log` | project | all | logs | file | high | 14d | Fatal error report the HotSpot JVM writes to the working directory when it crashes |
| jvm | `replay_pid*.log` | project | all | logs | file | high | 14d | Compiler replay data the JVM writes after a compiler crash; only useful for a bug report |
| rotated-logs | `*.log.[0-9]*`, `*.log.gz` | project | all | logs | file | medium | 14d | Rotated or compressed log archives (logrotate, log4j, winston); the live *.log file is deliberately not matched and archived history may still be wanted |
| pytest | `.pytest_cache` | project | all | cache | dir | high | 14d | pytest cache of last-failed and node ids; recreated by the next run |
| mypy | `.mypy_cache` | project | all | cache | dir | high | 14d | Incremental type check cache; rebuilt by the next mypy run |
| ruff | `.ruff_cache` | project | all | cache | dir | high | 14d | Lint and format result cache; rebuilt by the next ruff run |
| hypothesis | `.hypothesis` | project | all | cache | dir | high | 14d | Example database of failing inputs; regenerated by running the tests, though saved failures are lost |
| nox | `.nox` | project | all | cache | dir | high | 14d | Virtual environments of nox sessions; recreated by the next nox run |
| eslint | `.eslintcache` | project | all | cache | file | high | 14d | Lint result cache; rebuilt by the next eslint run with --cache |
| stylelint | `.stylelintcache` | project | all | cache | file | high | 14d | Lint result cache; rebuilt by the next stylelint run with --cache |
| jest | `.jest-cache` | project | all | cache | dir | high | 14d | Custom Jest cacheDirectory; rebuilt by the next jest run. The default jest_<hash> cache lives in the OS temp directory and cannot be matched safely, so it is omitted |
| sass | `.sass-cache` | project | all | cache | dir | high | 14d | Compiled stylesheet cache of legacy Ruby Sass; rebuilt by the next compile |
| coverage | `coverage` | project | all | cache | dir | medium | 14d | Coverage report directory of Jest, c8 and Istanbul; regenerated by the next coverage run, but the name is generic so confidence is medium |
| coverage | `.nyc_output` | project | all | cache | dir | high | 14d | Raw coverage data of nyc; regenerated by the next coverage run |
| coverage | `htmlcov` | project | all | cache | dir | high | 14d | HTML report of coverage.py; regenerated by coverage html |
| coverage | `.coverage` | project | all | cache | file | high | 14d | Data file of coverage.py; regenerated by the next coverage run |
| pip | `$XDG_CACHE_HOME/pip` | user | linux | cache | dir | high | 14d | Download and wheel cache of pip; pip rebuilds it and downloads packages again on demand |
| pip | `~/Library/Caches/pip` | user | darwin | cache | dir | high | 14d | Download and wheel cache of pip; pip rebuilds it and downloads packages again on demand |
| pip | `%LOCALAPPDATA%/pip/Cache` | user | windows | cache | dir | high | 14d | Download and wheel cache of pip; pip rebuilds it and downloads packages again on demand |
| poetry | `$XDG_CACHE_HOME/pypoetry/cache`, `$XDG_CACHE_HOME/pypoetry/artifacts` | user | linux | cache | dir | high | 14d | Package download and artifact caches of Poetry (virtualenvs are not matched); Poetry downloads and builds packages again on demand |
| poetry | `~/Library/Caches/pypoetry/cache`, `~/Library/Caches/pypoetry/artifacts` | user | darwin | cache | dir | high | 14d | Package download and artifact caches of Poetry (virtualenvs are not matched); Poetry downloads and builds packages again on demand |
| poetry | `%LOCALAPPDATA%/pypoetry/Cache/cache`, `%LOCALAPPDATA%/pypoetry/Cache/artifacts` | user | windows | cache | dir | high | 14d | Package download and artifact caches of Poetry (virtualenvs are not matched); Poetry downloads and builds packages again on demand |
| uv | `$XDG_CACHE_HOME/uv` | user | linux | cache | dir | high | 14d | Package and wheel cache of uv; uv downloads and builds packages again on demand |
| uv | `$XDG_CACHE_HOME/uv` | user | darwin | cache | dir | high | 14d | Package and wheel cache of uv; uv downloads and builds packages again on demand |
| uv | `%LOCALAPPDATA%/uv/cache` | user | windows | cache | dir | high | 14d | Package and wheel cache of uv; uv downloads and builds packages again on demand |
| yarn-cache | `$XDG_CACHE_HOME/yarn` | user | linux | cache | dir | high | 14d | Downloaded package archives of Yarn; Yarn downloads them again on demand |
| yarn-cache | `~/Library/Caches/Yarn` | user | darwin | cache | dir | high | 14d | Downloaded package archives of Yarn; Yarn downloads them again on demand |
| yarn-cache | `%LOCALAPPDATA%/Yarn/Cache` | user | windows | cache | dir | high | 14d | Downloaded package archives of Yarn; Yarn downloads them again on demand |
| yarn-cache | `~/.yarn/berry/cache` | user | all | cache | dir | high | 14d | Global package archive cache of Yarn Berry; Yarn downloads the archives again on demand |
| pnpm-store | `$XDG_DATA_HOME/pnpm/store` | user | linux | cache | dir | low | 14d | Content-addressable package store of pnpm; files are hard-linked into node_modules, so clearing it costs a full re-download on the next install |
| pnpm-store | `~/Library/pnpm/store` | user | darwin | cache | dir | low | 14d | Content-addressable package store of pnpm; files are hard-linked into node_modules, so clearing it costs a full re-download on the next install |
| pnpm-store | `%LOCALAPPDATA%/pnpm/store` | user | windows | cache | dir | low | 14d | Content-addressable package store of pnpm; files are hard-linked into node_modules, so clearing it costs a full re-download on the next install |
| npm-cache | `~/.npm/_cacache` | user | linux | cache | dir | low | 14d | Content-addressable download cache of npm; npm downloads packages again on demand (npm cache clean --force is the official way) |
| npm-cache | `~/.npm/_cacache` | user | darwin | cache | dir | low | 14d | Content-addressable download cache of npm; npm downloads packages again on demand (npm cache clean --force is the official way) |
| npm-cache | `%LOCALAPPDATA%/npm-cache/_cacache` | user | windows | cache | dir | low | 14d | Content-addressable download cache of npm; npm downloads packages again on demand (npm cache clean --force is the official way) |
| go-build | `$XDG_CACHE_HOME/go-build` | user | linux | cache | dir | low | 14d | Build and test result cache of the go command; rebuilt on demand, and go clean -cache is the official alternative |
| go-build | `~/Library/Caches/go-build` | user | darwin | cache | dir | low | 14d | Build and test result cache of the go command; rebuilt on demand, and go clean -cache is the official alternative |
| go-build | `%LOCALAPPDATA%/go-build` | user | windows | cache | dir | low | 14d | Build and test result cache of the go command; rebuilt on demand, and go clean -cache is the official alternative |
| macos | `.DS_Store` | project | all | os-junk | file | high | 0d | Finder view metadata; recreated when the folder is opened again |
| macos | `._*` | project | all | os-junk | file | low | 0d | AppleDouble sidecar of extended attributes; only clutter on non-macOS volumes (exFAT, FAT, network shares) and normally absent on APFS, so review before removing |
| windows | `Thumbs.db`, `ehthumbs.db` | project | all | os-junk | file | high | 0d | Explorer thumbnail cache; recreated when the folder is opened again |
| windows | `desktop.ini` | project | all | os-junk | file | low | 0d | Explorer folder customisation; may carry a folder icon or localized name the user set, so review before removing |
| editors | `*.swp`, `*.swo` | project | all | os-junk | file | medium | 0d | Vim swap files; a fresh one means the file is open in an editor or a session crashed with unsaved changes, so files open in a process are flagged |
| editors | `*~` | project | all | os-junk | file | medium | 0d | Backup copies left by Emacs, gedit and Vim |
| editors | `.#*` | project | all | os-junk | file | medium | 0d | Emacs lock files, present while the file is being edited |
| editors | `#*#` | project | all | os-junk | file | medium | 0d | Emacs auto-save files, present while the file is being edited or after a crash |
| crash-dumps | `core`, `core.[0-9]*` | project | all | crash | file | medium | 14d | Unix core dump of a crashed process (verified: ELF ET_CORE or Mach-O MH_CORE header); only useful for debugging that crash |
| crash-dumps | `*.dmp` | project | all | crash | file | medium | 14d | Windows minidump of a crashed process (verified: `MDMP`, `PAGEDUMP` or `PAGEDU64` signature); only useful for debugging that crash |
| crash-dumps | `*.stackdump` | project | all | crash | file | medium | 14d | Cygwin and MSYS stack dump of a crashed process |
| crash-dumps | `~/Library/Logs/DiagnosticReports/node-20[0-9][0-9]-*`, `~/Library/Logs/DiagnosticReports/java-20[0-9][0-9]-*`, `~/Library/Logs/DiagnosticReports/python*-20[0-9][0-9]-*`, `~/Library/Logs/DiagnosticReports/go-20[0-9][0-9]-*`, `~/Library/Logs/DiagnosticReports/Code-20[0-9][0-9]-*`, `~/Library/Logs/DiagnosticReports/Code Helper*-20[0-9][0-9]-*` | user | darwin | crash | file | medium | 14d | macOS crash reports of dev tools only (node, java, python, go, VS Code), matched by process prefix and date; other apps' reports are never matched |

### Covered elsewhere or skipped

| Location | Status | Why |
| --- | --- | --- |
| `.tox` | covered by build-artifacts (#32) | Listed there as `.tox` with the marker `tox.ini` or `pyproject.toml`, so no directory is reported by two detectors with different IDs. |
| `.parcel-cache` | covered by build-artifacts (#32) | Listed there as `.parcel-cache` with the marker `package.json`. |
| `node_modules/.vite/vitest` | skipped | Unreachable: the scanner never descends into `node_modules`, and `node_modules` itself is covered by build-artifacts. |
| `jest_<hash>` | skipped | The default Jest cache lives in the OS temp directory and its name cannot be matched safely with the glob format; only the explicit `.jest-cache` is listed. |
| Docker and testcontainers leftovers | skipped | Docker data is directories and images managed by the daemon, and testcontainers keeps only a configuration file (`~/.testcontainers.properties`), which is not clutter. |
| Poetry `virtualenvs` | skipped | Environments of projects may be in use; only Poetry's `cache` and `artifacts` are listed. |
| `*.ips` (blanket) | skipped | Would match crash reports of every application; only named dev tool prefixes are listed. |

## Build artifacts (`build_artifacts.json`)

`internal/catalog/data/build_artifacts.json` lists dependency and build output
directories for the `build-artifacts` detector. It has its own schema and its
own strict decoder, `catalog.BuildArtifacts()`; the tools loader never reads it
(only the files in `toolFiles` are loaded) and `BuildArtifacts()` rejects a file
in the tools format, because unknown fields are errors in both decoders.

```json
{
  "schema_version": 1,
  "entries": [
    {
      "id": "rust-target",
      "ecosystem": "rust",
      "dir": "target",
      "markers": ["Cargo.toml"],
      "marker_mode": "any",
      "confidence_cap": "high",
      "description": "Cargo build output; rebuilt by cargo build"
    }
  ]
}
```

| Field | Values |
| --- | --- |
| `id` | Unique kebab-case id, reported as evidence. |
| `ecosystem` | Kebab-case tool family (`node`, `rust`, `python`, ...); becomes `Tool` of the finding. |
| `dir` | Directory name or a glob on the base name (`*.egg-info`), or `parent/name` (`.angular/cache`, at most two segments). |
| `markers` | File names or globs that must exist next to the directory (in its parent). Empty means no marker is required. |
| `marker_mode` | `any` (default, one marker is enough) or `all` (every marker must exist; used for `vendor`). |
| `confidence_cap` | `high` (default), `medium` or `low`: the highest confidence the entry can produce. |
| `require_file_inside` | Optional literal file name that must exist inside the directory (`pyvenv.cfg` for virtual environments). |
| `description` | Required, one sentence on what it is and how it regenerates. |

Generic names (`dist`, `build`, `out`, `bin`, `obj`, `target`, `deps`) always
carry markers, `vendor` is capped at `low` and needs both files of a lock pair,
and `.terraform` is capped at `medium`. When several entries share a `dir`, the
first one whose markers fit wins, so put specific entries (`gradle-build`)
before generic ones (`build`). How findings are produced from these entries is
described in [build-artifacts.md](build-artifacts.md).

## How to contribute an entry

1. Find the tool's real behaviour and note a documentation URL (or issue or
   source link) that proves where it writes the files. The `source` URL is
   required for every entry.
2. Add the tool to `internal/catalog/data/ai_tools.json` (AI tools) or
   `dev_tools.json` (everything else), or add entries to the existing tool.
   Choose the narrowest `patterns`, an exact `kind` (never `any`), a
   `confidence` and `min_age_days` following the table above.
3. Protect the tool's configuration files (settings, instructions, skills,
   commands, hooks) with `protect` rules for both scopes where they exist.
4. Prefer wildcard-free directories or narrow globs; keep `**` for cases that
   need it.
5. Add a test case for a new pattern shape or OS filter to
   `internal/catalog` (existing tests already validate all embedded data:
   `go test ./internal/catalog`).
6. Run `go test ./...`; a typo in the JSON or a violated rule fails loading with
   a message naming the file, tool id and entry.

## AI tool entries

`data/ai_tools.json` covers the tools below. Every entry cites the source it
was derived from (official documentation or the tool's source code; where a
tool is closed source and its docs were not reachable, a source that shows the
path in code, named in the "Notes on sources" list). Policy: transcripts,
sessions and history are `medium` with `min_age_days: 30`; caches and logs are
`high` with 14 days; the generic run-log entry is `low`. Unverified per-OS
paths are omitted rather than guessed. `$XDG_CONFIG_HOME` and `$XDG_DATA_HOME`
below are the catalog variables, so they fall back to `~/.config` and
`~/.local/share`. "Code" and "Cursor" editors share the VS Code layout
(`<user data>` is `$XDG_CONFIG_HOME/<Name>` on Linux, `~/Library/Application
Support/<Name>` on macOS and `%APPDATA%/<Name>` on Windows).

| Tool | Removed (medium 30d unless noted) | Protected | Deliberately left out and why |
| --- | --- | --- | --- |
| Claude Code (`claude-code`) | `~/.claude/projects/*/*.jsonl` transcripts, `~/.claude/file-history/*`, `~/.claude/todos/*` (legacy). High 14d: `~/.claude/shell-snapshots/*`, `~/.claude/debug/*`, `~/.claude/statsig/*` (legacy) | Project: `CLAUDE.md`, `CLAUDE.local.md`, `.mcp.json`, `.claude/{settings.json,settings.local.json,commands,agents,skills,hooks,rules,agent-memory,agent-memory-local}`, `.claude/worktrees`, `.worktrees`. User: `~/.claude.json`, `~/.claude/{CLAUDE.md,settings*.json,keybindings.json,.credentials.json,commands,agents,skills,hooks,rules,plugins,agent-memory,history.jsonl}`, `~/.claude/projects/*/memory` | `.claude/worktrees` and `.worktrees` are deliberately excluded: they hold git worktrees that may contain uncommitted or unpushed work and are owned by the worktrees detector (#15) and its dirty/locked/unpushed checks. `paste-cache`, `backups`, `session-env`, `plans`, `history.jsonl` and subagent/tool-result folders are not listed (config-adjacent or not verified as safe on their own). |
| Cursor (`cursor`) | High 14d: `<user data>/logs/*`, `CachedData/*`, `Cache/*` per OS. Medium: `<user data>/User/workspaceStorage/*` (can contain chat history), `~/.cursor/projects/*/agent-transcripts/**/*.{jsonl,txt}` | `User/{settings.json,keybindings.json,snippets,mcp.json}`, `User/globalStorage/{state.vscdb*,storage.json}`, `~/.cursor/{mcp.json,rules,commands,skills,extensions,cli-config.json,argv.json}`, project `.cursorrules`, `.cursor/{rules,mcp.json,commands,skills}`, `.cursorignore`, `.cursorindexingignore` | `~/.cursor/chats/**/store.db` (a database that mixes sessions and state) and `globalStorage` (accounts, tokens). |
| Aider (`aider`) | Project: `.aider.chat.history.md`, `.aider.input.history` (medium 30d), `.aider.tags.cache.v*/` (high 14d) | `.aider.conf.yml`, `.aiderignore`, `.env`, `.aider.model.settings.yml`, `.aider.model.metadata.json`, `CONVENTIONS.md`, and the same files in `~` | `--llm-history-file` (no default, user chosen). |
| GitHub Copilot (`github-copilot`) | High 14d: `<Code user data>/logs/*/window*/exthost/GitHub.copilot-chat/*.log` | `~/.config/github-copilot` and `%LOCALAPPDATA%/github-copilot` entirely (`hosts.json`, `apps.json`, tokens), `~/.copilot`, Code `User/{settings.json,keybindings.json,snippets,prompts,mcp.json,globalStorage/github.copilot-chat}`, `.github/{copilot-instructions.md,instructions,prompts,agents,chatmodes}`, `.vscode/mcp.json` | Log and cache locations below `github-copilot` and `globalStorage/github.copilot-chat`: not verified, and the directories hold credentials. |
| OpenAI Codex CLI (`codex-cli`) | `~/.codex/sessions/**/*.jsonl` (medium 30d), `~/.codex/log/*` (high 14d) | `~/.codex/{config.toml,auth.json,AGENTS.md,AGENTS.override.md,prompts,skills,rules,history.jsonl}`, project `AGENTS.md`, `.codex` | `archived_sessions` (archived on purpose), `history.jsonl` (name not verified), a custom `CODEX_HOME` (not expressible with catalog variables). |
| Gemini CLI (`gemini-cli`) | `~/.gemini/tmp/*/chats/*`, `~/.gemini/tmp/*/checkpoints/*` (medium 30d), `~/.gemini/tmp/*/logs/*` (high 14d) | `~/.gemini/{settings.json,GEMINI.md,oauth_creds.json,google_accounts.json,trustedFolders.json,installation_id,.env,commands,extensions,policies,skills,agents,history}`, project `GEMINI.md`, `.gemini`, `.geminiignore` | `~/.gemini/history` shadow git repositories of checkpoints (protected). |
| Continue (`continue`) | `~/.continue/sessions/<id>.json` (medium 30d), `~/.continue/index/*` and `~/.continue/logs/*` (high 14d) | `~/.continue/{config.json,config.yaml,config.ts,.continuerc.json,.continuerc,rules,prompts,mcpServers,assistants,.env}`, `sessions/sessions.json` (session list), project `.continue`, `.continuerc*` | `dev_data` (content not verified). |
| Cline (`cline`) | `<Code or Cursor user data>/User/globalStorage/saoudrizwan.claude-dev/tasks/*` (medium 30d) | `globalStorage/saoudrizwan.claude-dev/settings`, `~/Documents/Cline`, `~/.cline`, project `.clinerules`, `.clineignore`, `.cline` | Other VS Code variants (Insiders, VSCodium) and the extension `cache`. |
| Roo Code (`roo-code`) | `<Code or Cursor user data>/User/globalStorage/rooveterinaryinc.roo-cline/tasks/*` (medium 30d) | `globalStorage/rooveterinaryinc.roo-cline/settings` (MCP servers, custom modes), `~/.roo`, project `.roo`, `.roomodes`, `.roorules`, `.rooignore` | Same as Cline. |
| Windsurf (`windsurf`) | High 14d: `<user data>/{logs,CachedData,Cache}/*` per OS | `User/{settings.json,keybindings.json,snippets,mcp.json}`, `User/globalStorage/{state.vscdb*,storage.json}`, `~/.codeium/windsurf` entirely (memories, global rules, MCP), project `.windsurf`, `.windsurfrules`, `.codeiumignore` | `workspaceStorage` (Cascade history location not verified). |
| Goose (`goose`) | Linux only: `~/.local/share/goose/sessions/*.jsonl` (medium 30d), `~/.local/state/goose/logs/*` (high 14d) | `~/.config/goose` (`config.yaml`, `permission.yaml`, `secrets.yaml`, prompts), `%APPDATA%/Block/goose/config`, `.goosehints`, `.goose`, `AGENTS.md` | macOS and Windows session and log paths: not verified. |
| OpenHands (`openhands`) | `~/.openhands/sessions/*` (medium 30d, default local file store) | `~/.openhands/{*.json,config.toml,.env,microagents,skills,.jwt_secret}`, project `.openhands`, `.openhands_instructions` | Other file store locations (`conversations`) and hosted deployments. |
| Amp (`amp`) | Linux and macOS: `$XDG_DATA_HOME/amp/threads/*.json` (medium 30d) | `$XDG_CONFIG_HOME/amp` entirely, `$XDG_DATA_HOME/amp/{secrets.json,session.json}`, `AGENTS.md`, `AGENT.md`, `.agents/{skills,commands}` | Windows path not verified; recent Amp builds keep threads in the cloud, so the local directory may be empty. |
| Zed (`zed`) | `<data dir>/threads/threads.db*` (medium 30d), `Zed.log.old` (high 14d; logs dir is `~/Library/Logs/Zed` on macOS, `<data dir>/logs` elsewhere) | `$XDG_CONFIG_HOME/zed`, `~/.config/zed` (macOS), `%APPDATA%/Zed`, prompts, prompt overrides, external agents, project `.zed`, `.rules`, `AGENTS.md` | The live `Zed.log` (open by the running editor). |
| Generic run logs (`agent-run-logs`) | Low, 30d: `*.jsonl` below `.agent/{runs,logs}`, `.agents/{runs,logs}`, `.agent-runs` at the project root | `.agent` and `.agents` skills, rules, commands and memory | Everything else: a heuristic for conventional scratch directories, not a tool documentation. |

### Notes on sources

- Claude Code: `code.claude.com/docs/en/claude-directory` (application data
  table, legacy directories, auto memory below `projects/<project>/memory`).
- Cursor, Windsurf, Cline, Roo Code and Copilot are extensions or forks of
  VS Code, whose source defines `logs`, `CachedData` and `User/workspaceStorage`
  below the user data directory. Cursor, Windsurf and Copilot paths come from
  community documentation and source in public repositories (linked in each
  entry) because the vendor documentation was not reachable when the entries
  were written; a follow-up should re-check them against vendor docs.
- Amp and OpenHands paths come from open source clients that read those
  stores (linked in each entry), not vendor documentation.
- The generic `agent-run-logs` directories are a naming convention, not
  something a vendor documents; that is why they are `low`.
