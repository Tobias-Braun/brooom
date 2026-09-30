# Brooom architecture

Brooom is a single static Go binary. The CLI is the product; a future
dashboard (`brooom serve`) and agent layer are pure clients of the same core
packages and the same findings schema.

## Principles (non-negotiable)

1. **Safety first.** Every command is a dry run unless `--apply` is given.
   Applying asks for confirmation per group or item unless `--yes`. Nothing
   is ever deleted silently.
2. **Scoped by default.** Without flags only the git repository containing
   the working directory is scanned. `--workspaces` scans every repository and
   project folder below the configured roots. Every path is made absolute,
   symlink-resolved and checked against the allowed locations by
   `scope.Guard`; anything outside is refused.
3. **Detectors find, actions act.** Detectors never modify anything (no file
   writes, no git commands that take locks). Actions consume findings.
4. **Reversible by default.** Files go to the OS trash (or quarantine);
   branches are deleted with `git branch -d` unless `--force`; worktrees are
   removed with `git worktree remove`; git maintenance uses conservative
   expiries. Every applied session writes a manifest for `brooom undo`.
5. **Fast.** Parallel walking, skip lists, cached directory sizes invalidated
   by mtime.

## Data flow

```
             ┌──────────── config (~/.brooom/config.json + per-root + .brooom.json)
             │
 cwd / roots ─┴─► scope (FindRepoRoot / Discover) ─► []Target
                                                        │
                   detectors (internal/detectors/*) ◄───┤  detect.Run: parallel
                   read-only, use gitx + walk + Guard   │  (target × detector)
                                                        ▼
                                              []findings.Finding
                                                        │
                    ┌───────────────────────────────────┼─────────────────────┐
                    ▼                                   ▼                     ▼
          output formatters                 action.Executor            future dashboard /
   (table tree json ndjson plain summary)   plan → dry-run/confirm      agent (read JSON)
                                             → apply → session manifest
                                                        │
                                                        ▼
                                  trash (OS trash / quarantine / delete), git
```

## Packages

| Package | Responsibility |
| --- | --- |
| `cmd/brooom` | `main`: calls `cli.Main`. Nothing else. |
| `internal/cli` | Cobra commands, one file per command (`cmd_<name>.go`). Flag parsing, wiring, exit codes. No detection or cleanup logic. |
| `internal/buildinfo` | Version/commit/date via ldflags, fallback to embedded VCS info. |
| `internal/config` | Config types, `Default()`, load/save/validate, per-root overrides, tighten-only `.brooom.json`, `~/.brooom` layout (`BROOOM_HOME` overrides). |
| `internal/scope` | Repo detection, workspace discovery (`[]Target`), `Guard` path validation (symlinks, `..`, case-insensitive filesystems, Windows drive letters/UNC). |
| `internal/walk` | Parallel walker with skip lists, `DirSize` with mtime-invalidated cache in `~/.brooom/cache`. |
| `internal/gitx` | Read-only-safe git runner (`GIT_OPTIONAL_LOCKS=0`, C locale, no prompts) and git helpers behind a per-repo `Repo` handle (branches and upstreams, base detection, merge detection incl. squash/rebase via patch-id, remote containment, worktrees, dirty check, open PRs via `gh`), `Pipe` (streams one git command into another without buffering, for history scans) and `Repo.Memo` (per-repo memoization of expensive measurements); `Cache` shares memoized handles per scan (`detect.Env.Repos`), uncached `Open` is for actions. |
| `internal/findings` | **The findings schema** (see [findings.md](findings.md)): `Finding`, `Report`, IDs, risk flags, totals. Stable contract. |
| `internal/detect` | `Detector` interface, registry, `Env`, parallel `Run` engine. |
| `internal/detectors/<name>` | One package per detector, self-registering via `init()`. `internal/detectors/all` blank-imports them. |
| `internal/catalog` | Embedded JSON data: AI tool locations, dev tool log/cache locations, build artifact dirs + project markers. Extensible via config. |
| `internal/action` | `Action` interface, registry, `Executor` (plan → dry run / confirm → apply → manifest → summary). One file per action type. |
| `internal/trash` | `Trasher` interface; OS trash per OS (`trash_windows.go`, `trash_darwin.go`, `trash_unix.go` freedesktop), quarantine, delete. |
| `internal/session` | Session manifests in `~/.brooom/sessions`, listing, undo bookkeeping. |
| `internal/output` | Formatters, one file per format, registered by name. Color/TTY handling helpers. |
| `internal/procs` | "Is this file open by a process?" per OS (best effort, never blocks a scan). |
| `internal/updatecheck` | The opt-in update check: latest-release lookup, semver compare, install-method detection, 24h cache. The only package allowed to import `net/http` (enforced by a test). |
| `internal/testutil` | Deterministic throwaway git repos and file trees for tests. |

## Contracts

### Detectors (`internal/detect`)

```go
type Detector interface {
    Name() string            // kebab-case, e.g. "merged-branch"
    Description() string
    Category() Category      // git, files, ai, logs, artifacts
    Detect(ctx, env *Env, target scope.Target, emit func(findings.Finding)) error
}
```

Rules:
- Never modify anything. Git calls go through `env.Git` (sanitized env).
- Resolve every emitted path through `env.Guard.Resolve` (or
  `ResolveParent` for symlinks that must not be followed).
- Use `env.Now` and `env.AgeDays` for ages, never `time.Now()`.
- Read thresholds from the effective config for the target.
- Set blocking risk flags honestly; a finding with a blocking flag must
  suggest `ActionNone` and explain why in `SuggestedAction.Reason`.
- Build IDs with `findings.NewID(detector, kind, path, ref)`.
- Evidence codes are stable snake_case; messages are short sentences.
- Return an error only for failures of the whole target; skip unreadable
  entries silently or via evidence.
- `stale-branch` skips branches that `merged-branch` reports (a branch is
  reported once, as merged).

A detector that needs user-level targets (catalog locations below the home
directory) additionally implements the optional `detect.TargetSource`
(`ExtraTargets(ctx, cfg) ([]scope.Target, error)`). The scan pipeline calls it
once per scan for every selected, globally enabled detector, appends the
`TargetUser` targets (deduplicated, missing locations dropped) and allows
their paths in the guard. It only declares locations and never detects.

Detector names (config keys, `--detector` values): `stale-branch`,
`merged-branch`, `worktrees`, `git-bloat`, `large-untracked`,
`ai-artifacts`, `log-and-runtime-files`, `build-artifacts`.

### git-bloat (`internal/detectors/gitbloat`)

Reports `git-loose-objects` (loose count above `loose_objects_threshold`),
`git-packs` (pack count above `pack_count_threshold`), `git-reflog` (size of
`<common-dir>/logs` plus `worktrees/*/logs` above `reflog_threshold_bytes`) and
`git-large-blob` (history blobs of at least `large_blob_bytes`, 0 disables the
scan).

- Savings are documented estimates, labelled `estimated_savings` in evidence:
  50% of the loose size (gc packs with delta compression), 10% of the pack
  size. The reflog size is an `upper_bound`, because the size of entries older
  than `reflog_expire` is not knowable without expiring. Exact values are
  measured by the action with `count-objects` before and after.
- Loose objects and packs are cleaned by `git gc --prune=<prune_expire>`. That
  also expires reflog entries per `gc.reflogExpire` and
  `gc.reflogExpireUnreachable` (defaults 90/30 days); the finding's reason says
  so. `git prune` is not a separate finding.
- Large blobs have no action: removing them means rewriting history
  (`git filter-repo`), which Brooom does not do. At most the 20 largest per
  repository are reported (evidence `truncated`), with `size_bytes` 0. The scan
  pipes `rev-list --objects --all` into `cat-file --batch-check` (`gitx.Pipe`)
  and is bounded by a 20 s deadline per repository; on timeout the blob
  findings are dropped and everything else is still reported.
- All findings sit on the repository's main worktree and measurements are
  memoized per common dir (`gitx.Repo.Memo`), so every linked worktree target
  yields the same IDs and does not rescan.

### Actions (`internal/action`)

`Plan` re-validates each finding at apply time (re-resolve path, re-stat,
re-check open files / dirty state / new commits) and returns a `Step` or an
`ErrSkipped`-wrapped reason; `findings.Actionable(flags, force)` decides
whether risk flags allow acting. `Apply` returns a `session.Entry` with undo
information. `Undo` reverses an entry where possible. The `Executor` owns
confirmation, manifests and the summary; individual actions never prompt.

#### The `trash` action

`internal/action/trash.go` removes files, directories and symlinks through the
configured `trash.Trasher`. `Plan` re-validates in this order and skips with a
reason at the first failure:

1. `Guard.ResolveParent` (the final element is kept, so symlinks are removed
   as links and never followed); outside the allowed roots is refused.
2. Static refusals on the resolved path: filesystem/volume roots, allowed
   roots, repository roots, `.git` or anything inside it, the Brooom home
   (and anything containing it) and its `sessions` and `quarantine` dirs, and
   the user's home directory (and anything containing it).
3. Existence and contents: one `walk.Walk` pass with `Fresh: true` sums the
   size exactly like `walk.DirSize` and looks for a `.git` entry (file or
   directory) at any depth below a directory. A nested repository, linked
   worktree or submodule refuses the whole directory. A directory that cannot
   be read completely is refused too, since an unreadable subtree could hide
   a repository. `SizeBytes` and `LastModified` are refreshed in the step's
   finding copy, whose `Path` is the resolved path.
4. Open files (`procs.OpenFiles`): an open path is refused.
   `ErrUnavailable`, `ErrIncomplete` (for `false` entries) and other errors
   mean unknown and are allowed, but noted in the step description.
5. Blocking risk flags via `findings.Actionable`.
6. Tracked files: `git ls-files -z -- <path>` in the repository root; tracked
   files, or a failing check, skip unless `--force`.
7. Delete-strategy guard: with the `delete` strategy a finding whose
   `Meta["user_data_risk"]` is `untracked` is refused.

Not overridable by `--force`: steps 1, 2, 3, 4 (open files, also via the
`file_open_by_process` flag) and 7. `--force` only lifts blocking risk flags
and the tracked-files check. `Apply` re-resolves and re-checks the static
refusals, removes the re-resolved path and returns a manifest entry with the
trash record; a path that vanished is a skip, not a failure. `Undo` restores
with the strategy recorded in the entry (`Env.TrasherFor`), never the
configured one, after checking that the original path lies inside the allowed
roots; `ErrRestoreConflict` and `ErrNotRestorable` are passed through.

### Trash (`internal/trash`)

`Remove(path) (Record, error)` / `Restore(Record)`. Never follows symlinks.
Cross-device moves fall back to copy + verify + delete. Windows locked files
produce a clear error naming the file.

The OS trash is selected per platform by `newOSTrasher` in
`ostrash_unix.go` (freedesktop), `ostrash_darwin.go` and
`ostrash_windows.go`; quarantine and delete live in `quarantine.go` and
`delete.go`, so the platform implementations never touch each other's files.

### Sessions (`internal/session`)

One manifest per applied run, `~/.brooom/sessions/<id>.json` (mode 0600, dir
0700), ids like `20260929-224501-3f9a`. `Manifest` holds `version`, `id`,
`started_at`, `finished_at` (zero if the run crashed), `command`, `entries[]`
and `reclaimed_bytes` (sum of `size_bytes` of `applied` entries only; call
`RecomputeReclaimed` after changing statuses). Each `Entry` records status
(`applied`, `failed`, `skipped`, `restored`), action, path, size, the trash
`Record` or `undo` data, `restorable` and a manual `recovery_hint`.

`Store.Save` writes atomically (temp file in the same dir, fsync, rename), so
a crash never leaves a half-written manifest; `*.tmp` files are ignored.
`Load` takes a full id or unique prefix (`ErrNotFound`, `ErrAmbiguous`; ids
with separators or `..` are refused). `List` returns manifests newest first
plus `[]Problem` for unreadable, corrupt or unsupported-version files, so one
damaged file never hides the history. `brooom sessions [id] [--format json]`
is the read-only view.

### Open files (`internal/procs`)

`procs.OpenFiles(ctx, paths)` reports which paths (or directories with an
open file below them) are open by a process. Best effort with a bounded
timeout (`DefaultTimeout` when the context has no deadline). `ErrUnavailable`
and `ErrIncomplete` (partial map still returned, `true` entries reliable) mean
unknown for `false` entries, never "safe". Per OS: `/proc/<pid>/fd` on Linux,
`lsof` on macOS, Restart Manager on Windows.

### Output (`internal/output`)

`Formatter.Write(w, *findings.Report, Options)`. `json` is the `Report` as
is; `ndjson` is one `Finding` per line; `plain` is paths only (for branches:
`<repo>\t<branch>`), one per line. Formats never write ANSI codes when
`Options.Color` is false.

## Configuration

`~/.brooom/config.json` (override the home with `BROOOM_HOME`). Every field
has a default in `config.Default()`, so the file only contains overrides.
Layout of `~/.brooom`:

```
config.json
cache/        scan cache (safe to delete)
sessions/     <session-id>.json manifests
quarantine/   <session-id>/... quarantined files
```

Per-root overrides live in `roots[].thresholds` / `roots[].detectors`. A
repository may contain `.brooom.json` that can only tighten rules (disable
detectors, raise thresholds, add protected branches and excludes).

The full key reference, merge semantics, validation rules and the
`ForTarget`/exclude contract for detectors are in [config.md](config.md).

## Conventions

- Go 1.24, `CGO_ENABLED=0`, no runtime dependencies. Keep third-party
  dependencies minimal (cobra, golang.org/x/sys, golang.org/x/term).
- Cross-platform: use `filepath`, never hard-code `/`; OS-specific code in
  `_windows.go` / `_darwin.go` / `_unix.go` files with build tags as needed;
  every package must build for linux, darwin and windows on amd64 and arm64.
- Tests: table-driven, `t.TempDir()`, `testutil.NewRepo` for git. Tests must
  pass on Linux, macOS and Windows (CI runs all three). Never touch the real
  home directory: set `BROOOM_HOME` / `HOME` / `XDG_DATA_HOME` to temp dirs.
- CLI post-run steps: cobra runs only one `PersistentPostRun` per command
  chain (the nearest one), so the root owns the single hook and it only
  iterates `a.postRunHooks`. Features (update notice, and later #26) must
  append to `a.postRunHooks` and never assign a command's `PersistentPostRun`,
  or they would silently replace each other. Hooks run in registration order
  after a successful command only.
- Background work (update check): it is best effort, bounded by a short grace
  period at exit, records each attempt in its cache so a slow or failing
  network costs at most one request per backoff, and never fails a command.
- Errors: wrap with context (`fmt.Errorf("...: %w", err)`), name the path.
- Comments explain *why*; doc comments on every exported identifier.
- Functions stay below cyclomatic complexity 15 (`gocyclo`).
- `go vet`, `golangci-lint run` and `go test ./...` must pass before a PR.

## Exit codes

`0` success, `1` error, `2` usage error.
