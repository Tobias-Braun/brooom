# The build-artifacts detector

`build-artifacts` (`internal/detectors/buildartifacts`) finds dependency and
build output directories such as `node_modules`, `target`, `.venv`, `.next` and
`dist`, which are the biggest disk hogs in inactive projects. The directories
and their project markers are data, see
[build_artifacts.json in catalog.md](catalog.md#build-artifacts-build_artifactsjson).
The detector only reports; the `trash` action removes.

## What is reported

- Artifact directories are located per target (repository or project folder)
  with one walk that never descends into a matched directory, `.git`, a
  configured `scan.skip_dirs` name, a directory excluded by the root's or the
  repository's `exclude` globs, or a nested repository (a subdirectory with its
  own `.git` entry, which is another scope; target that repository directly to
  clean it). Nested artifacts (`node_modules/foo/node_modules`, `__pycache__`
  inside `.venv`) are therefore never reported separately.
- Markers are looked up next to the candidate, in its parent directory, so
  monorepo packages work (`packages/a/node_modules` next to
  `packages/a/package.json`). Marker names compare case-insensitively on macOS
  and Windows.
- `dist`, `build`, `out`, `bin`, `obj`, `target`, `vendor` and `deps` without a
  marker are never reported, and `.venv`, `venv` and `env` only when they
  contain `pyvenv.cfg`. The single exception is `node_modules` without a
  `package.json`: a stray install, reported at `medium` with the evidence
  `marker_missing`.
- Symlinked artifact directories (pnpm-style links) are reported with the
  `symlink` risk flag and size 0, and are never followed; only the link would
  be removed. A symlink whose rule needs to look inside the directory
  (virtual environments) is not reported.
- Size comes from `walk.DirSize` (cached in `~/.brooom/cache`); findings below
  `thresholds.min_size_bytes` are dropped. `last_modified` is the newest mtime
  inside the directory, which may come from the cache; this is acceptable
  because generated output is rarely edited in place.

## Confidence and inactivity

Confidence follows the inactivity of the project, not the age of the artifact.
The project directory is the parent of the artifact. Its last activity is the
newer of

1. the last commit touching the project directory
   (`git log -1 --format=%ct -- <dir>`; no commits or no git means no signal),
2. the newest mtime of any regular file in the project directory outside
   artifact directories, excluded directories, nested repositories and `.git`.

Generated directories never count as activity, because installing or building
touches them without anyone working on the project. Results are cached per
project directory, so several artifacts of one project cost one walk.

| Situation | Confidence | Flags and evidence |
| --- | --- | --- |
| Last activity at least `inactive_days` ago | `high` | `project_inactive_days` (days) |
| Activity newer than that | `medium` | `recently_modified`, `project_active` (days) |
| No commit and no source file at all | `medium` | `project_activity_unknown` |
| Stray `node_modules` (no marker) | `medium` | `marker_missing` |

Every result is then capped by the entry's `confidence_cap` (`vendor` `low`,
`.terraform` `medium`). `recently_modified` is also set when the artifact
directory itself changed within `thresholds.recent_days`. Further evidence:
`matches_catalog`, `ecosystem`, `marker`, `last_commit`, `tracked_files`,
`gitignored` and `not_gitignored` (a hint that `git status` shows the
directory). A `gitignored` directory also carries the informational risk flag
of the same name. `not_gitignored` is evidence only and never a risk flag:
the artifact directories are regenerable by definition and the default
strategies (`trash`, `quarantine`) are reversible. Only the permanent
`delete` strategy refuses directories that hold untracked, non-ignored files.

## Tracked files and Force

When git tracks any file below the artifact (`git ls-files -z -- <path>`), the
finding gets the blocking flag `tracked_files` and its suggested action is
`none` with the reason that removing it would show up as deletions. Only when
`env.Force` is true (`--force`) is `trash` suggested, with the reason
`forced: contains files tracked by git`; the confidence is unchanged either way.
A failing `git ls-files` counts as tracked. The `trash` action re-checks this at
apply time. Targets that are plain project folders have no git state: no
`tracked_files`, no `last_commit`, and inactivity uses file times only.

## Unreadable directories

When part of the artifact cannot be read (for example a `chmod 000`
subdirectory), `walk.DirSummary.Incomplete` is set and the size is only a lower
bound. The finding is kept, with evidence `unreadable` and the suggested action
`none` (reason "cannot read part of the directory"), because the `trash` action
refuses such directories at apply time anyway.

## Claim matcher

`buildartifacts.Claims(dir)` returns a pure predicate `func(rel string, isDir
bool) bool` that applies exactly the rules of the detector (catalog and config
overrides through `ClaimsWith`, markers looked up in the parent of `rel` under
`dir`). It reads directory listings only: no sizes, no git, no writes. The
detector uses the same matcher, so the two cannot diverge. The
`large-untracked` detector uses it to skip directories this detector owns.

## Configuration

`detectors.build-artifacts.enabled`, `inactive_days` (default 30), `dirs` and
`extra_dirs`; the list syntax is documented in [config.md](config.md).
