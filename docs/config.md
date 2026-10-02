# Configuration reference

Brooom works with zero configuration inside a repository. The optional file
`~/.brooom/config.json` (override the home with `BROOOM_HOME`) only needs to
contain what differs from the built-in defaults (`config.Default()`).
`brooom config init` (see #38) writes the complete document.

Implementation: `internal/config` (`Load`, `Save`, `SaveFull`, `Marshal`,
`Validate`, `ForTarget`, `LoadRepoConfig`, `ApplyRepoConfig`).

## Loading rules

- A missing file yields the defaults. Any other read error (permissions, path
  is a directory) is an error naming the path.
- The file is decoded strictly: duplicate keys (at any depth), unknown keys, wrong types, trailing data after
  the JSON value and files over 1 MiB are errors. An empty file is an error
  (likely a truncated write); `{}` is valid. A UTF-8 BOM is tolerated.
- Errors name the full key path, for example
  `config.json: unknown key "detectors.merged-branch.mdoe" (did you mean "mode"?)`
  or `config.json: git.protected_branches[1]: expected string, got number`. Syntax errors
  report line and column.
- `version`: missing means the current version (1). A newer version is
  rejected with a request to upgrade Brooom; `version < 1` is rejected.

### Merge semantics

Values are merged over the defaults: a field that is absent keeps its default.
**Slices and maps present in the file replace the default entirely; they are
never merged.** For example `git.protected_branches` in the file is the
complete list of protected branches, and `trash.per_detector` is not combined
with defaults. A JSON `null` for a slice or map yields an empty one.

`Save` writes only values that differ from the defaults (plus `version`), so
later changes of a default reach users who never customised that value; lists
and maps are written whole. `SaveFull` / `Marshal(cfg, true)` render the
complete document. Saving is atomic (temp file in the same directory, fsync,
mode `0600`, rename) and never writes an invalid configuration.

## Keys

| Key | Default | Meaning |
| --- | --- | --- |
| `version` | `1` | File format version. |
| `roots[]` | | Removed. The root registry of earlier releases is ignored (a non-empty list prints a one-line note); pass a folder as the path argument instead, e.g. `brooom sweep ~/code`. |
| `thresholds.min_age_days` | `14` | Findings younger than this are not reported. Honoured by `ai-artifacts` and `log-and-runtime-files` (file age, unless the detector or catalog entry sets its own), and, as a floor, by `stale-branch` and `worktrees` (own `min_age_days` = max of both), `merged-branch` (tip commit age) and remote merged branches. The branch and worktree detectors apply it (`Thresholds.AgeFloor`) only when it is raised above the built-in `14`: a value of `14` or lower never floors `stale-branch`, `worktrees` or `merged-branch`, so the default never hides a recently merged branch or lowers a deliberately short per-detector age. `build-artifacts` (`inactive_days`) and `git-bloat` have no age filter; `large-untracked` reports files whatever their age. |
| `thresholds.min_size_bytes` | `0` | Findings smaller than this are not reported. Sizes are allocated bytes including the blocks of the directories themselves, so an empty directory has a non-zero size (typically one filesystem block, 4 KiB) and is only hidden by a `min_size_bytes` above that. Honoured by `ai-artifacts`, `log-and-runtime-files`, `build-artifacts` and, as a floor over its own `min_size_bytes`, `large-untracked`. Branch, worktree and `git-bloat` findings have no size filter (`git-bloat` uses its own count and byte thresholds). |
| `thresholds.recent_days` | `2` | Window for the `recently_modified` risk flag. Informational only; it never withholds or lowers a worktree removal. |
| `git.protected_branches` | `main, master, develop, dev, trunk, release/*, release-*, gh-pages` | Branch globs never suggested for deletion. Must not be empty. |
| `git.base_branches` | `main, master, develop, trunk` | Candidate base branches for merge detection. Must not be empty. |
| `git.use_gh` | `true` | Query open PRs through `gh` when available. This is a network call (the only one in detection), bounded by a 5 s timeout per call; after the first timeout, network error or missing `gh` (even after earlier calls succeeded) the rest of the scan skips it and open-PR status is reported as unknown, which never blocks a scan. Set `false` for a fully offline run. `gh` runs with the same sanitized environment as git (no inherited `GIT_DIR` and similar). |
| `detectors.stale-branch` | enabled, `min_age_days` 90, `include_unpushed` | Stale branch detector. |
| `detectors.merged-branch` | enabled, `mode` `ancestor+squash`, `include_remote` false | `mode` is `ancestor` or `ancestor+squash`. |
| `detectors.worktrees` | enabled, `include_stale`, `min_age_days` 0 | Worktree detector. `min_age_days` is the abandonment threshold of the stale rule only and defaults to 0 (no age threshold, stale rule off): merged and patch-equivalent detached worktrees are removable whatever their age, so cleanup can run right after a large agent run. No preset raises it. |
| `detectors.git-bloat` | enabled, thresholds, `reflog_expire` `90.days.ago`, `prune_expire` `2.weeks.ago` | The two expiry values are passed to git as option values: they must be non-empty, contain no whitespace or control characters and must not start with `-`. Reflog expiry never touches the stash reflog (`refs/stash`); stash entries are uncommitted work. The `everything` sweep preset shortens an expiry to `90.days.ago` only when it is longer (`N.days.ago`, `N.weeks.ago`, `now`, `never` are compared; other forms are left as configured). |
| `detectors.large-untracked` | enabled, `min_size_bytes` 100 MiB, `include_ignored` | Large untracked files. |
| `detectors.ai-artifacts` | enabled | Optional `tools`, `extra[]` catalog entries, `min_age_days`. The per-repository data of the scanned repositories below the home directory (Claude Code transcripts) is always included; `user_locations` of earlier releases is ignored (`true` prints a note). User-scope `extra` entries are not scanned: only built-in tools with a `repo_key` have per-repository locations. |
| `detectors.build-artifacts` | enabled, `inactive_days` 30 | Optional `dirs`, `extra_dirs`, both lists of `name` or `name:marker1,marker2` (see below). |
| `detectors.log-and-runtime-files` | enabled | Optional `categories`, `extra[]`, `min_age_days`. Global caches and logs in the home directory are not cleaned; `user_locations` of earlier releases is ignored (`true` prints a note). |
| `trash.strategy` | `trash` | `trash`, `quarantine` or `delete`. |
| `trash.per_detector` | | Strategy per detector name. |
| `trash.quarantine_retention_days` | `14` | Quarantined sessions older than this are purged. **`0` means never purge**; negative is invalid. |
| `trash.allow_delete` | `false` | Must be `true` for any use of `delete` (default or per detector). |
| `output.format` | `table` | `table`, `tree`, `json`, `ndjson`, `plain`, `summary`. |
| `output.color` | `auto` | `auto`, `always`, `never`. |
| `scan.concurrency` | `0` | Parallel walkers (0 = number of CPUs). |
| `scan.cache` | `true` | Enables the scan cache directory (`~/.brooom/cache`). Today only the gitbloat blob scan uses it (keyed on refs and packs). Size and age measurements of detectors always re-read the tree, because file ages must be exact. `brooom purge` removes unused cache files. |
| `scan.skip_dirs[]` | | Plain directory names (no separators, not `.`/`..`). |
| `scan.max_depth` | `6` | Workspace discovery depth. |
| `agent.provider` | | `""`, `anthropic` or `openai-compatible`. |
| `agent.endpoint`, `agent.model` | | Agent settings. |
| `agent.api_key_env` | | Name of the environment variable holding the key (`[A-Za-z_][A-Za-z0-9_]*`); keys are never stored. |
| `sweep.preset` | `everything` | Preset of `brooom sweep` when no preset argument is given: `after-agents`, `tidy` or `everything`. The old names `safe`, `standard` and `aggressive` still load and run `everything`. |
| `update_check` | `false` | Opt-in update check. |

**Quarantine and volumes (Windows).** Quarantine lives in `<home>\quarantine`, by default below `%USERPROFILE%`. A project on another volume (say `D:\`) cannot be renamed into it, so it is copied, verified and then removed, which is slow, needs free space on the home volume and is refused for trees holding junctions (pnpm/npm workspaces) or files that a process has open. Set `BROOOM_HOME` to a directory on the same volume as your projects (for example `D:\.brooom`) and quarantine moves become plain renames. Brooom has no per-volume quarantine directory: one home keeps `undo`, `purge` and the retention notice simple.

### Catalog `extra` entries

Entries in `detectors.ai-artifacts.extra` and
`detectors.log-and-runtime-files.extra` need a unique kebab-case `id`, a
non-empty `name` and at least one location in `project`, `user` or `entries`.
`project` paths are relative to the project and must not contain `..`. The
optional `category`, `homepage`, `entries` and `protect` fields use the catalog
format; the catalog validates them when it loads the extras. See
[catalog.md](catalog.md).

### Build artifact `dirs` and `extra_dirs`

Both lists of `detectors.build-artifacts` take entries of the form:

- `name`: every directory called `name` (a name or a glob on the base name such
  as `*.egg-info`, or `parent/name` such as `.angular/cache`) is reported, with
  no marker required. Writing the bare name is the explicit opt-in to
  "no marker".
- `name:marker1,marker2`: the directory is only reported when at least one of
  the markers (file names or globs) exists next to it, in its parent directory.

`dirs` replaces the default list. A bare name that the embedded catalog knows
keeps the catalog's marker rules and confidence cap (`"dirs": ["dist"]` scans
only marker-gated `dist`); an unknown bare name is marker-less. `extra_dirs`
adds to whatever list is in effect, and a bare name there is always
marker-less, even if the catalog gates it. Entries from the config get the
ecosystem `custom` and are capped at `high`. Empty names, `..`, backslashes,
more than two segments and invalid globs are rejected by validation. See
[build-artifacts.md](build-artifacts.md).

## Paths: `~` and environment variables

`ExpandPath` expands a leading `~`, `~/` or `~\` (home from
`os.UserHomeDir`, so `HOME`/`USERPROFILE` overrides apply), `$VAR`, `${VAR}`
and on Windows `%VAR%`. An undefined or empty variable is an error: it never
expands to an empty string, which would turn `$WORK/x` into `/x`. `~user` is
not supported. The path argument of the CLI is expanded the same way.

## Validation

`Validate()` returns a `*ValidationError` (matches `errors.Is(err,
config.ErrInvalid)`) listing every problem as `Problems[]{Field, Message}` in
deterministic order. It checks:

- all thresholds and detector numbers non-negative;
- allowed values for `output.format`, `output.color`, `trash.strategy`,
  `trash.per_detector`, `detectors.merged-branch.mode`, `agent.provider`;
- any use of `delete` requires `trash.allow_delete: true`;
- `trash.quarantine_retention_days >= 0` (0 = never purge);
- `git.protected_branches` / `base_branches` non-empty valid globs without
  control characters; the git expiry values as described above;
- `scan.concurrency`, `scan.max_depth >= 0`, plain `scan.skip_dirs` names;
- `agent.api_key_env` is an environment variable name;
- catalog `extra` entries as described above.

## Effective configuration: `ForTarget`

`cfg.ForTarget(target)` returns the configuration for a directory `target`:
the global configuration overlaid with `<target>/.brooom.json`. The receiver is
never modified. A relative target is an error.

### Exclude contract for detectors

The effective config carries `RepoExclude`, never stored in a file: the
`.brooom.json` `exclude` globs, relative to the target. Detectors must skip
directories matching them using `scope.Excluded`. `internal/config` only
validates and carries the patterns.

## Per-repo `.brooom.json`

The file lives in the repository root and is **untrusted input**: a
repository may come from anywhere. It can only make Brooom more careful.

```json
{
  "disable": ["build-artifacts"],
  "thresholds": { "min_age_days": 30, "min_size_bytes": 1048576, "recent_days": 7 },
  "protected_branches": ["hotfix/*"],
  "exclude": ["generated", "**/fixtures"]
}
```

- Loading is strict with the same key-path errors, limited to 64 KiB. A
  missing file is fine; a file that is a symlink or not a regular file is
  refused (so a repository cannot make Brooom read arbitrary files).
- `disable`: known detector names only (unknown is an error); sets
  `enabled: false`.
- `thresholds`: values may only be raised or equal; a lower value is an error
  (`.brooom.json: thresholds.min_age_days: 7 is lower than the effective value
  30; repo config may only tighten`), as are negatives. A raised
  `min_age_days` also floors the per-detector ages (`stale-branch`,
  `worktrees`, `ai-artifacts`, `log-and-runtime-files`) and a raised
  `min_size_bytes` floors `large-untracked.min_size_bytes` (new value = max of
  existing and repo value).
- `protected_branches` are appended to the global list (deduplicated, order
  kept). `exclude` globs are validated and collected in `RepoExclude`.
- Everything else (enabling detectors, `trash`, removing protected
  branches, ...) cannot be expressed and is rejected by strict decoding with
  the key path.

## External command timeouts

The timeouts are fixed, not configurable. Every git call and every `Pipe`/`PipeLimit` pipeline whose context has no deadline is bounded by 10 minutes, so a hung git (slow remote, stuck hook) cannot stall a scan; a call that hits the bound fails with `git <command> timed out after <duration>`. Maintenance actions (`git gc`, `prune`, `reflog expire`) are bounded by 6 hours instead, because repacking a very large repository legitimately takes long. `gh` is bounded by 5 seconds per call (see `git.use_gh`). A deadline on the caller's context always wins.
