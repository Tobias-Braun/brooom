# Brooom architecture

Brooom is a single static Go binary. The CLI is the product; a future
dashboard (`brooom serve`) and agent layer are pure clients of the same core
packages and the same findings schema.

## Principles (non-negotiable)

1. **Safety first.** Every command that changes something shows its plan and
   asks once (`Proceed? [y/N]`) before it acts; `--dry-run` stops after the
   plan, `--yes` skips the question, and without a terminal an unanswered
   question is a usage error. Nothing is ever deleted silently, and `sweep`
   never acts on unmerged or uncommitted work.
2. **Scoped by default.** Without a path only the git repository containing
   the working directory is scanned. A folder given as the path argument is
   walked and every repository and project folder below it is scanned. Every
   path is made absolute,
   symlink-resolved and checked against the allowed locations by
   `scope.Guard`; anything outside is refused.
3. **Detectors find, actions act.** Detectors never modify anything (no file
   writes, no git commands that take locks). Actions consume findings.
4. **Reversible by default.** Files go to the OS trash;
   branches are deleted with `git branch -d`; `-D` is used only when base ancestry, or remote containment (alone or together with a squash/rebase merge), is re-verified at apply time, or with force, which only `brooom review` sets for an item the user chose to delete (a squash/rebase merge of commits on no remote needs it); worktrees are
   moved to the trash and deregistered from git (never a bare `git worktree
   remove`, which would delete ignored files permanently); git maintenance uses conservative
   expiries. Every applied session writes a manifest for `brooom undo`.
5. **Fast.** Parallel walking, skip lists, a git blob scan cache keyed on refs
   and packs, and an optional mtime-invalidated `DirSize` cache (unused by
   detectors, which need exact ages).

## Data flow

```
             ┌──────────── config (~/.brooom/config.json + .brooom.json)
             │
 cwd / path ─┴─► scope (FindRepoRoot / Discover) ─► []Target
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
                                           trash (OS trash), git
```

## Packages

| Package | Responsibility |
| --- | --- |
| `cmd/brooom` | `main`: calls `cli.Main`. Nothing else. |
| `internal/cli` | Cobra commands, one file per command (`cmd_<name>.go`). Flag parsing, wiring, exit codes. No detection or cleanup logic. |
| `internal/buildinfo` | Version/commit/date via ldflags, fallback to embedded VCS info. |
| `internal/config` | Config types, `Default()`, load/save/validate, tighten-only `.brooom.json`, `~/.brooom` layout (`BROOOM_HOME` overrides), notes for keys of earlier releases (`Config.Deprecated`). |
| `internal/scope` | Repo detection, workspace discovery (`[]Target`), `Guard` path validation (symlinks, `..`, case-insensitive filesystems, Windows drive letters/UNC). |
| `internal/walk` | Parallel walker with skip lists, `DirSize`, optionally with an mtime-invalidated cache in `~/.brooom/cache` for callers that accept lower-bound ages (no detector does today). |
| `internal/gitx` | Read-only-safe git runner (`gitx.Env`: repository-selecting `GIT_*` variables such as `GIT_DIR`/`GIT_INDEX_FILE` and inherited `GIT_CONFIG_*` are stripped, `GIT_OPTIONAL_LOCKS=0`, `GIT_NO_LAZY_FETCH=1` (git >= 2.44), `core.fsmonitor=false`, C locale, no prompts, a 10 minute default timeout for contexts without a deadline, also for `Pipe`/`PipeLimit`, 6 hours for maintenance via `gitx.WithTimeout`; a passed bound is a `*TimeoutError` that matches `context.DeadlineExceeded`; only promisor/lazy-fetch errors in `MergedInto` mean not merged, other object errors such as corruption are returned; `gh` runs with the same sanitized environment) and git helpers behind a per-repo `Repo` handle (branches and upstreams, base detection, merge detection incl. squash/rebase via patch-id, remote containment, worktrees, dirty check, open PRs via `gh`), `Pipe`/`PipeLimit` (stream one git command into another without buffering, for history scans; `PipeLimit` and `ExecRunner.MaxOutput` kill the process(es) past a byte cap and return `ErrOutputLimit`; squash detection streams `log -p`/`diff` into `patch-id` this way, capped at 64 MiB, and rebase detection never matches a branch containing merge commits, only the squash net-diff check can) and `Repo.Memo` (per-repo memoization of expensive measurements); `Cache` shares memoized handles per scan (`detect.Env.Repos`), uncached `Open` is for actions. On cached handles ancestry is answered for all branches by one `for-each-ref --merged`, patch ids are computed once per commit, and one scan-wide breaker stops calling `gh` after its first timeout or network failure, also after earlier successes; every external command has a `WaitDelay` so a grandchild holding a pipe cannot outlive a deadline. |
| `internal/findings` | **The findings schema** (see [findings.md](findings.md)): `Finding`, `Report`, IDs, risk flags, totals. Stable contract. |
| `internal/detect` | `Detector` interface, registry, `Env`, parallel `Run` engine. |
| `internal/detectors/<name>` | One package per detector, self-registering via `init()`. `internal/detectors/all` blank-imports them. |
| `internal/catalog` | Embedded JSON data: AI tool locations, dev tool log/cache locations, build artifact dirs + project markers. Extensible via config. |
| `internal/presets` | Sweep presets as pure data (detector set, minimum confidence, config overlay) plus `Apply` (deep copy, never mutates the loaded config). Presets never touch safety settings. |
| `internal/action` | `Action` interface, registry, `Executor` (plan → dry run / confirm → apply → manifest → summary). One file per action type. |
| `internal/trash` | `Trasher` interface; OS trash per OS (`ostrash_windows.go`, `ostrash_darwin.go` with `mactrash.go`, `ostrash_unix.go` freedesktop, `ostrash_other.go`/`ostrash_native_other.go` for the rest), `empty.go` for `brooom empty-trash`. |
| `internal/session` | Session manifests in `~/.brooom/sessions`, listing, undo bookkeeping. |
| `internal/output` | Formatters, one file per format, registered by name. Color/TTY handling helpers. |
| `internal/progress` | The terminal-free `Reporter` interface the engine, executor and undo report progress through, plus the no-op `Nop` and the `progresstest.Recorder` for tests. |
| `internal/cli/progressui` | The live stderr display: a bubbletea model (spinner, progress bar, lipgloss styles) and the `Display` reporter that owns its lifecycle. The only package that imports the Charm libraries. |
| `internal/procs` | "Is this file open by a process?" per OS (best effort, never blocks a scan). |
| `internal/testutil` | Deterministic throwaway git repos and file trees for tests, and `DirTrasher`, the stand-in for the OS trash in tests. |

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
- `merged-branch` hides a branch as unstarted only when it sits on the base
  tip, was never pushed and its reflog holds at most the creation entry
  (`gitx.Repo.BranchCreatedOnly`); a fast-forward-merged branch has more
  entries and is reported; `Branch: renamed` entries are ignored, so a renamed
  fresh branch stays hidden.
- Branch detectors never drop a branch on a git failure: per-branch errors are
  collected and returned joined (a non-fatal `ScanError`) while the other
  findings are still emitted; early returns keep the collected errors, and
  `stale-branch` reports a failing merged check once per repository (first
  branch named, the others counted).
- Stale-branch separates the safety gate (`UnpushedCount`: commits on no
  remote, evidence `unpushed_commits`) from the number shown to users
  (`UniqueCount`: commits only this branch holds, evidence `unique_commits`).
  An upstream that is a local branch (`Branch.UpstreamRef` under `refs/heads/`,
  remote ".") is checked against that ref and never counts as a push.

A detector that needs user-level targets (the per-repository catalog
locations below the home directory) additionally implements the optional
`detect.TargetSource` (`ExtraTargets(ctx, cfg, repos) ([]scope.Target,
error)`, where `repos` are the repository targets of the scan). The scan
pipeline calls it once per scan for every selected, globally enabled
detector, appends the
`TargetUser` targets (deduplicated by declaring detector, tool and resolved
path, so tools sharing a base directory are all scanned; missing locations dropped) and allows
their paths in the guard. It only declares locations and never detects.

The `ai-artifacts` detector (`internal/detectors/aiartifacts`) matches the
`ai` category of the catalog. Project targets get one pruned `walk.Walk`
(`.git`, `scan.skip_dirs`, the shared `scope.ProjectSkipDirs` list (also
used by `log-and-runtime-files`), matched directories, root and repo excludes
and nested repositories are not descended into). Catalog
`protect` patterns always win: a candidate that is protected, below a
protected path or a directory containing one is dropped, as is a matched
directory containing a `.git` entry at any depth (`walk.DirSummary.HasVCS`,
gathered by the fresh size pass). User-level targets are the
repository-keyed locations of the scanned repositories (`catalog.RepoLocations`,
#288), declared for every scan; findings there are entries inside a location,
never the location itself. `tracked_files` is the only
blocking flag `--force` lifts; an open file keeps the action at `none`.

The `log-and-runtime-files` detector (`internal/detectors/logs`) mirrors
`ai-artifacts` for the catalog categories `logs`, `cache`, `os-junk` and
`crash` (toggled by `detectors.log-and-runtime-files.categories`; it has no
user-level targets, global caches are not cleaned). The walk, protect and
nested-repository rules are the
same (the code is duplicated locally on purpose; extracting a shared helper is
a later cleanup). Differences: the size pass is `Fresh` because logs are written
in place; a recently modified `*.log` / `*.log.N` file drops from high to medium
confidence; and one batched `procs.OpenFiles` per target flags files a process
has open with `file_open_by_process`, which is blocking and never overridable,
so those findings suggest `none` even with `--force`. `tracked_files` is
force-overridable and then suggests `trash` with a `forced: ...` reason.

Crash-dump catalog entries carry a `verify` content
check (ELF core / MDMP header); a candidate that fails it is dropped.

Detector names (config keys, `--detector` values): `stale-branch`,
`merged-branch`, `worktrees`, `git-bloat`, `ai-artifacts`,
`log-and-runtime-files`, `build-artifacts`.

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
  and is bounded by a 20 s deadline per repository; on timeout or failure the
  blob findings are dropped, everything else is still reported and the gap
  is returned as a scan error (never silence). Successful scans are cached in
  the scan cache dir, keyed by ref tips (`refs/replace` included), HEAD, pack set,
  the alternates, shallow and grafts files and the threshold; objects inside an
  alternate store are not fingerprinted (documented limitation). A failed scan
  is memoized per repository and reported by the first target only, so linked
  worktrees do not repeat the error.
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
OS `trash.Trasher` (`Env.Trasher`). `Plan` re-validates in this order and skips with a
reason at the first failure:

1. `Guard.ResolveParent` (the final element is kept, so symlinks are removed
   as links and never followed); outside the allowed roots is refused.
2. Static refusals on the resolved path: filesystem/volume roots, allowed
   roots, repository roots, VCS metadata (`.git`, `.hg`, `.jj`, `.svn`, by
   `walk.IsVCSName`) or anything inside it, the Brooom home
   (and anything containing it) and its `sessions` dir, and
   the user's home directory (and anything containing it).
3. Existence and contents: one `walk.Walk` pass with `Fresh: true` sums the
   size exactly like `walk.DirSize` and looks for VCS metadata (`.git` as file
   or directory, `.hg`, `.jj`, `.svn`; `walk.IsVCSName`) or a bare git
   repository shape (a directory holding `HEAD`, `objects/` and `refs/`;
   `walk.DirShape`) in the target and at any depth below it. A nested
   repository, linked worktree, submodule or bare clone refuses the whole
   directory. The detectors use the same predicate (`walk.HasVCSEntry`) to
   prune nested repositories, and `walk.DirSummary.HasVCS` (cache version 3)
   carries it through the size cache. A directory that cannot
   be read completely is refused too, since an unreadable subtree could hide
   a repository. `SizeBytes` and `LastModified` are refreshed in the step's
   finding copy, whose `Path` is the resolved path.
4. Open files (`procs.OpenFiles`): an open path is refused.
   `ErrUnavailable`, `ErrIncomplete` (for `false` entries) and other errors
   mean unknown and are allowed, but noted in the step description.
5. Blocking risk flags via `findings.Actionable`.
6. Tracked files: `git ls-files -z` with literal pathspecs in the repository
   root; tracked files, or a failing check, skip unless `--force`. The
   question is asked once per repository for all trash targets of a run
   (`gitx.TrackedUnder`, chunked pathspecs matched back in Go), not once per
   finding: one call at plan time and one live call at the start of `Apply`
   (after the confirmation), so a run costs two calls per repository instead
   of a multiple of the finding count. A git failure marks every target
   unknown. A single target keeps the plain per-path call. The `logs` and
   `ai-artifacts` detectors use the same helper for their `tracked_files` risk.
7. Catalog protection (`protect.go`): the catalog protect rules (`.env`,
   `.mcp.json`, `CLAUDE.local.md`, `.claude/settings.local.json`, ..., and the
   user-level tool configuration) are enforced on the path itself and on what
   a directory contains, whatever detector or kind the finding names. Below a
   directory only path-anchored patterns count (`ProjectMatcher.ProtectedAnchored`):
   base-name patterns such as `.npmrc` would match inside every `node_modules`.
   The rules are tried relative to each directory from the project root down
   to the target's parent, since outside a repository the root is only the
   allowed root. Extras from the config can only add rules; tool toggles never
   switch a protection off.

Windows specifics: `Guard.ResolveParent` canonicalises the final element with
`GetLongPathName` (8.3 aliases such as `GIT~1` become `.git`), and step 2 ends
with `RefuseByIdentity`, which compares file identity (`identityOf`) of the
path and its ancestors with `.git`, the Brooom home, the user's home and the
sessions dir. Step 3
treats reparse-point directories that are not name surrogates (OneDrive,
ProjFS) as directories (`walk` decides by the reparse tag); a junction or other
directory the walker cannot inspect is refused, so trashing a junction is never
possible. Detectors classify entries with the same rule through
`walk.IsDirNoFollow` / `walk.IsDirEntry` instead of `os.Lstat(...).IsDir()`.
`walk.Walk` never descends into any VCS metadata directory. Git directories
that are not named `.git` (bare repositories such as `proj/.bare`, the target of
a `.git` link file, the common dir of a linked worktree) are refused by
`refuseGitDir`, which examines every ancestor up to the allowed root; `--force`
never lifts it, and neither does git reporting a path as "outside repository".
Bare layout (`proj/.git` file with `gitdir: ./.bare` plus linked worktrees):
discovery (`scope.isGitDir`) does not report such a folder as a repository, it
descends and reports the linked worktrees instead; `gitx.Repo.Anchor` returns
the first `git worktree list` entry even when bare, and the branch and
worktree detectors use it in place of the main worktree, so their findings
carry the bare directory as repository. `gitx.OpenAnchor` / `Cache.AnchorRepo`
are the only openers that accept the bare directory (actions use them; `Open`
still returns `ErrBareRepo`). Running inside the bare folder itself is a usage
error, not "Nothing to sweep".
The identity check fails closed: an entry that exists but whose identity cannot
be read counts as an alias and refuses the removal; only a missing entry is
"not the same". On Windows the identity comes from `CreateFile` plus
`GetFileInformationByHandle` (`fileid_windows.go`) rather than the lazy
`os.SameFile`, which answers false on any error.

Not overridable by force: steps 1, 2, 3, 4 (open files, also via the
`file_open_by_process` flag) and 7. Force only lifts blocking risk flags and
the tracked-files check. `Apply` re-resolves and re-checks the static
refusals, then re-runs the nested `.git`, open-file and tracked-files checks
against live state (a step may come from any caller, so neither its `Meta`
nor its risk flags are trusted). The nested `.git` check walks the tree a
second time. `Apply` removes the re-resolved path and returns a manifest entry
with the trash record; a path that vanished is a skip, not a failure. `Undo`
restores from the OS trash after checking that the original path lies inside
the allowed roots and is neither inside `.git` nor inside the Brooom home
(config, cache, sessions). Entries an earlier release removed with its
quarantine or delete strategy are reported as not restorable by `PlanUndo`,
with the location of a quarantined copy. That check is by name, then by file identity of every
existing ancestor, so aliases like 8.3 short names and symlinks are caught;
bind mounts or hard links of `.git` under another parent are not (the scope
guard is the defence there). `ErrRestoreConflict` and `ErrNotRestorable` are
passed through.

#### The `delete-branch` action

`internal/action/deletebranch.go` deletes with `git branch -d` and reads the
sha git reports as deleted, which is what the manifest records. Every `-D`
(chosen up front or escalated after git refuses `-d`) instead runs
`git update-ref -d refs/heads/<name> <verified tip>` after re-checking that no
worktree has the branch checked out, so a branch that moved after
re-validation is skipped ("branch moved during apply") and never deleted (the
checked-out check is best effort: a worktree created between it and
`update-ref` is not caught; the `-d` path is not compare-and-swap, git itself
refuses unmerged branches there);
`branch.<name>.*` is then removed like `git branch -D` does.

Safety policy for `-D` (decided in #178, pinned by `deletebranch_policy_test.go`):
what justifies `-D` is always re-derived from the repository, never from the
finding's `Detector` or `Args["verified"]`. Ancestry of the tip in the base is
a fact and needs nothing more. A merge found only by the patch-id heuristic
(squash or rebase) justifies `-D` solely when every commit of the branch is
also contained in a remote-tracking branch (`ContainedInRemotes`); a
squash-merged branch whose commits exist on no remote is skipped without
`--force` (the reason quotes `UniqueCount` and says the merge is only
heuristic), because a wrong guess would leave the work as unreachable objects
recoverable only by `brooom undo` or `git branch <name> <sha>` until the next
gc. With `--force` it is deleted with `-D` and the reason "forced; not verified
as merged". Remote containment alone still justifies `-D` (nothing is lost).
The refusal text is joined with semicolons only, since the commit-count phrase
can already carry a parenthesis.

Before deleting, `branch.<name>.remote/merge` are recorded in `Entry.Undo`
(`upstream_remote`, `upstream_merge`). Undo validates them (name shape, git's
`check-ref-format`), and only when it created the branch restores them with
`git branch --set-upstream-to` if the remote-tracking ref exists, else by
writing the two config keys.

Known limitation (deferred from #88, plan item 4): a local branch whose name
collides with a tag or a remote-tracking name (for example a local branch
called `origin/main`) is handled safely (detectors and actions address refs
fully qualified) but is not reported as an informational finding yet.

Base candidates (#204): `gitx.DefaultBase` still names the one primary base
(origin/HEAD, then origin/<name> before local <name>), and it stays the
default for safety decisions because a stale local main misses merges. Merge
detection, however, asks `gitx.BaseCandidates` / `MergedIntoAny`: a tip merged
into any existing candidate counts, ancestry against every candidate before any
patch-id guess. A match in a local candidate that ranks behind a remote primary
is marked `Base.Unpushed` and reported as "local main (not pushed)"; it is not
remote-verified, so `-D` still needs `ContainedInRemotes` exactly like a
heuristic merge. Both are answered by one `for-each-ref` over origin/HEAD and
every configured candidate (exact names only, annotated tags peeled like
`rev-parse <ref>^{commit}`), not one rev-parse per candidate, and the squash
pass of `MergedIntoAny` does not repeat the ancestry check of the first pass.

Names the action refuses (`gitx.RefusedBranchName`) are checked by merged-branch
and stale-branch too: such findings are not actionable and carry a quoted
`git update-ref -d refs/heads/<name>` hint. The recovery hint stored with a
deletion ignores branches that the same session deletes as well
(`Env.plannedDeletes`), and undo reports a ref hierarchy clash (`feat` versus
`feat/child`) as a conflict with a `git branch <name>-restored <sha>` hint.

Dubious ownership: `gitx.Open` returns `ErrNotRepo` only for git's "not a git
repository"; a repository git refuses because another user owns it is an
`*gitx.UnsafeRepoError` (`errors.Is(err, gitx.ErrUnsafeRepo)`) carrying git's
message and the `safe.directory` hint, and other failures keep their stderr.
`detect.Run` reports it once per repository as "skipped: dubious ownership ...",
and the worktree and branch actions report it as a visible skip.

#### The worktree actions

`internal/action/worktree.go` (plus `worktree_undo.go`, `worktree_prune.go`)
holds `remove-worktree` and `prune-worktrees`. Both run git in the repository
named by `Finding.Meta["repo"]` (resolved through `Guard.Resolve`, opened
uncached) and look worktrees up with `Repo.ListWorktrees` and `gitx.SamePath`.

`remove-worktree` skips when the path is no longer registered, is the main or
a bare worktree, is locked (never overridable, the reason is quoted), has a
rebase, merge, cherry-pick, revert or bisect in progress (never overridable;
`gitx.Worktree.Operation`, read from the worktree's private git directory) or
initialized submodules (`HasSubmodules`: `<admin>/modules` is not empty, which
makes `git worktree remove` fail; the detector reports such a worktree without
action and evidence `worktree_has_submodules`, trash-based removal is not
offered), its
directory is missing, HEAD or branch differ from the finding, the current
directory is inside it or a process has it open (`checkOpen`, before the dirty
handling and never overridable), it hits the trash action's static path
refusals (all but the repository-root one), a `.git` entry exists below the
worktree root (nested repository or submodule, measured with `measureWorktree`,
which ignores the worktree's own link file), or it is dirty without `--force`.

A branch that a paused rebase or bisect will return to (`head-name`,
`BISECT_START`) is checked out in no worktree while HEAD is detached, so
`gitx.Repo.ListBranches` sets its `WorktreePath` from the git directories
(`OperationBranch`); branch detectors then flag `current_branch` and
`delete-branch` refuses it.

`gitx.Open` enforces `MinGitVersion` per handle, `gitx.Cache.Repo` once per
cache: the version is resolved outside the cache-wide lock, only a success is
kept (a cancelled context fails that lookup and does not poison the scan) and
every new handle is pre-seeded with it; new repositories are resolved and
built outside the lock and inserted with a re-check. An older or unparseable
git is an error (`ErrGitTooOld`). On git older than
2.31, which has no `locked` porcelain token, `ListWorktrees` reads
`<common>/worktrees/<id>/locked` and treats a worktree whose state cannot be
determined as locked.

The `worktrees` detector protects active worktrees the same way (one batched
`procs.OpenFiles` call per scan covers all worktrees, and on macOS `procs`
answers all directories of a call from a single lsof listing, so the lsof runs
do not grow with the number of candidates): a candidate
containing the current directory or open by a process gets the blocking
`file_open_by_process` flag (evidence `worktree_in_use`, action `none`), and a
candidate modified within `thresholds.recent_days` (fresh mtimes) gets the
informational `recently_modified` flag and evidence only: no age threshold
applies to worktree removal (agent runs leave hundreds of fresh worktrees that
must be removable at once), so neither confidence nor action change.
`detectors.worktrees.min_age_days` defaults to 0 and no preset raises it; it
only feeds the stale (abandoned checkout) rule, which stays off at 0. A
detached HEAD that no ref contains counts as merged when all its commits are
patch-equivalent to the base (`gitx.Repo.MergedInto` on the HEAD sha, evidence
`head_patch_equivalent`, medium confidence, only in `ancestor+squash` mode);
`remove-worktree` re-verifies this at apply time (`checkDetachedRemovable`:
held by a ref, or patch-equivalent) and refuses genuinely unique commits, also
with `--force`. A worktree whose branch sits on the base tip, was never pushed and
only has its creation in the reflog ("unstarted", `gitx.Repo.Unstarted`, shared
with merged-branch) is never reported as merged. A worktree whose branch ref
does not resolve (all-zero HEAD) gets a low-confidence finding without action
(evidence `branch_ref_missing`, hint `git branch <name> <sha>` or
`git worktree repair`); an unborn `--orphan` worktree stays quiet. The directory always goes through the configured trasher
(`Trasher.Remove`), because `git worktree remove` would permanently delete files
git ignores (`.env`, agent settings, logs, build output). The plan flags
uncommitted and ignored content (`Repo.IgnoredEntries`, `git ls-files -o -i
--exclude-standard --directory`). Afterwards `git worktree remove -- <missing
path>` drops only that registration (never `worktree prune`, which would take
unrelated entries too); a failure keeps the trash record and `Restorable`.
Git older than the releases that accept a missing path may refuse that
command, so the action then falls back to deleting the one
`<common>/worktrees/<id>` directory whose `gitdir` file names the moved path
(refused when it holds a `locked` file, verified by a fresh worktree list).
The drift check (`checkUnmodified`) is skipped for findings whose
`Meta["mtime_source"]` is `commit` (the detector had no file mtime and used the
HEAD commit time); `walk` and a missing key are compared. The plan states the
counts of uncommitted and ignored entries.
The `delete` strategy refuses a dirty worktree or one with ignored files even
with `--force`; a worktree with neither is removed by plain `git worktree
remove` (git's `--force` is never passed). Undo re-adds plain removals (branch
form, else `--detach` at the recorded commit) and
restores trashed ones via a `--no-checkout` placeholder, `Trasher.Restore`,
`git worktree repair` and a mixed `reset` (staged/unstaged split is not kept).
`Entry.Undo` carries `worktree`, `branch`, `head` and `repo`.

Scope of a run from a linked worktree (#287): the whole repository. The linked
worktree and the main worktree are both repo targets and allowed locations
(`repoTargets`, `allowMainWorktree`), so a sweep run from one of the worktrees
an agent left behind also sees its siblings below the main checkout; branch
findings reached through both targets carry one ID and the engine keeps one,
and the worktree the command runs in is in use and never removed. A bare
repository (the `.bare` of a bare plus linked worktrees layout) has no working
tree and stays a metadata location only. The main worktree is also registered
with `Guard.WithRepoMeta`; `Guard.ResolveRepoMeta` accepts such a location as
that exact directory, never anything below it. `ResolveRepoMeta` is used only
to locate the repository: the branch and worktree detectors (`merged-branch`,
`stale-branch`, `worktrees`), `delete-branch` and the worktree actions. Git
maintenance
(`git-bloat`, `git-gc` and friends) uses `Resolve`, one operation per common
git dir. A linked worktree the guard does not allow (one outside the
repository, which is git's default for `git worktree add ../x`) is never
examined or offered; `worktrees` reports each existing one as an
informational finding (action `none`, low confidence, evidence code
`outside_scope`, message carrying `scope.OutsideWorktreeHint`: pass the folder
that holds the repository and its worktrees as the path), so it is visible
in every format that lists non-actionable findings (`plain` stays a path pipe
of actionable findings only). Its low confidence keeps it out of every sweep
preset, so only the blocked reasons of a sweep carry the hint. `Guard.OutsideNote` gives the hint only for a
non-empty path that fails with `ErrOutsideScope`. The `current_branch`
block reason of `merged-branch` and `stale-branch` names the
worktree and carries the same hint when it is out of scope. Missing (prunable)
worktrees are still reported whenever the repository is reachable.

`prune-worktrees` requires the recorded path to be prunable, unlocked and
missing on disk. Apply drops only the finding's own registration with
`git worktree remove --force -- <path>` (never the repository-wide `git worktree
prune`, which would also take registrations the user declined), then compares
the list before and after and fails if anything other than the target vanished.
A detached worktree whose HEAD no branch, remote branch or tag holds is skipped
at plan and apply time, because HEAD and its reflog live in the admin dir and
the commit would become unreachable; the `worktrees` detector likewise offers no
action for a missing detached worktree whose HEAD containment is unknown or
negative (`head_not_pushed`, `unpushed_commits`, hint `git worktree repair`).
The entry records `repo`, `worktree`, `branch` and `head` in `Undo` (for manual
recovery, `git branch rescue <sha>`), but it is not undoable.

### Sweep presets (`internal/presets`, `internal/cli/cmd_sweep.go`)

Sweep is the one cleaning command. `brooom sweep [preset]` resolves the preset
(the positional argument, then `sweep.preset`, then `everything`; the legacy
names `safe`, `standard` and `aggressive` resolve to `everything` with a note on
stderr, because `config init` wrote `safe` into every config) and calls
`runCleanup` with the preset's detectors, a keep filter built from its
confidence floors and an overlay. Presets are named by intent:

- `after-agents`: worktrees, merged-branch, ai-artifacts.
- `tidy`: log-and-runtime-files.
- `everything`: both plus build-artifacts and git-bloat. Build artifacts need
  high confidence (`Preset.Floors`): medium means the project is still being
  worked on, and trashing its `node_modules` is not what "everything" means.

No preset runs stale-branch: sweep never removes unmerged work, and it has
no `--force`. Skip reasons that used to say "re-run with
--force" point to `brooom review` instead.

### Emptying the OS trash (`internal/cli/cmd_emptytrash.go`, `internal/trash/empty.go`)

`brooom empty-trash` (#292) reads every session manifest and keeps the
applied, still restorable entries of the `trash` strategy whose stored copy
exists. `trash.VerifyStored` accepts a copy only strictly inside an OS trash
directory, judged by path components on any host (`InOSTrash`: `.Trash`,
`.Trashes`, the `files`/`info` folders of a freedesktop trash,
`$Recycle.Bin`), and only with the recorded type and, for a regular file, the
recorded allocated size; directories are compared by type, because a
cross-device move changes their allocation. Refused items are listed as kept
with the reason. After one confirmation `trash.RemoveStored` deletes the copy
and its `.trashinfo`/`$I` metadata, and `session.Store.MarkTrashEmptied` marks
the entries not restorable with a recovery hint. The user's own trash content
is never listed or touched.

### Review (`internal/cli/cmd_review.go`)

`brooom review [path]` is where unmerged and dirty work is decided on (#291).
It scans the worktrees and stale-branch detectors with `Env.Force` and an
overlay that withholds nothing for its age (`StaleBranch.MinAgeDays` 0,
`IncludeUnpushed`, `Worktrees.IncludeStale`), and keeps every stale branch and
every worktree with a blocking flag. Per item it shows the changed and
untracked files (`git status --porcelain`), the commits no remote-tracking
branch holds (`rev-list --count <rev> --not --remotes` and the first subjects),
the last activity and the blocking flags, then asks `[d]elete / [k]eep /
[q]uit` (enter keeps; q or the end of input discards every choice). Findings
without an action even under force (in use, locked, current or protected
branch) are listed as kept. The chosen findings go through the shared executor
with `yes` and `force` set, so each is re-planned against the live state, the
session records them and `brooom undo` restores worktrees (untracked files
included, from the trash) and branches (from the recorded tip). Without a
terminal, or with `--dry-run`, review only lists.

The executor shows the plan, asks `Proceed with N items (SIZE)? [y/N]` once
(`action.confirmer.confirm`) and acts on an explicit yes. When `action.Options.Select` is set (the CLI sets it
only when stdin and stdout are terminals, `app.planSelector`), the question
offers `e`: `internal/cli/checklist` (bubbletea, alternate screen) lists every
item ticked, and only the items still ticked on enter run; q, esc, ctrl+c,
ctrl+d, an error or unticking everything change nothing. Unticked items are
reported as "kept as you chose", not as skipped. The terminal is handed to
bubbletea as the `*os.File` itself, otherwise it does not switch to raw mode
and Enter never arrives; other readers are wrapped so the end of input aborts. The terminal check comes after planning, so a run with
nothing to do never needs an answer. `compact` becomes `action.Options.Brief`:
the plan is shown when the run asks, one line per group without items or
commands, and the summary is the one line of
`renderBriefSummary` (`internal/action/brief.go`): failures, a skipped count,
the undo line and, last, the counts per kind with the reclaimed size in bold
green (when color is on). The progress display's done line leaves the size
out, so it is printed once; `--dry-run` shows the full plan. A machine `--format` only reports, like
`--dry-run` (through `runScan`, the shared report path that also streams
`ndjson`), and is a usage error with `--yes`. The overlay is applied right
after `config.Load` in `newScanRequest` and before `ForTarget`, so root
overrides and the tighten-only `.brooom.json` still act on top of it. Findings
the keep filter rejects are dropped in `execute`, before reporting and
planning; blocked findings are not treated specially and stay blocked.
`everything` shortens the git expiries with `shorterExpiry`: `90.days.ago`
replaces a configured `reflog_expire` / `prune_expire` only when it is shorter
in the restricted comparison of `now`, `never`, `N.days.ago` and `N.weeks.ago`;
longer, equal and unparseable values are kept, so the default `2.weeks.ago`
prune expiry is never raised. Overlays only switch things off
(`include_stale` worktrees) and never touch age thresholds,
`RecentDays` or protected branches.
`--detector` is intersected with the preset; naming one outside it is a usage
error that names the preset that runs it. `config.PresetNames` and
`config.LegacyPresetNames` mirror the presets package (pinned by a test)
because `presets` imports `config`.

#### The git maintenance actions

`internal/action/gitmaint*.go` holds `git-gc` (`git gc --quiet --prune=<date>`),
`git-prune` (`git prune --expire=<date>`) and `git-reflog-expire`
(`git -c gc.refs/stash.reflogExpire=never -c gc.refs/stash.reflogExpireUnreachable=never
-c gc.reflogExpire=<date> reflog expire --all`, see `gitx.ReflogExpireArgs`). They destroy data that is
otherwise recoverable, so only the `everything` preset runs them, never
restorable (`Restorable=false`, `Undo` returns an
error wrapping `trash.ErrNotRestorable` with the explanation) and every
entry carries a `RecoveryHint` saying what was lost. Never `--force`,
`--aggressive` or `--cruft`; gc's own reflog expiry follows the user's git
config and is documented in the plan and help text.

Stashes are uncommitted user work: `git stash list` is the reflog of
`refs/stash` and the stash commits are reachable only through it, so expiring
that reflog deletes work. Git's per-ref settings `gc.refs/stash.reflogExpire`
and `...Unreachable` (the latter hits older entries) are therefore set to
`never` with `-c` for `reflog expire` and `gc` (`gitx.StashProtection`); they
only apply when no `--expire` option is given, so the date is passed as
`gc.reflogExpire`. Git's built-in default already spares stashes in gc, but a
user setting must not be able to change that. Plans count the older stash
entries that are kept (`gitx.StashExpiring`, a dry run) and never mention them
when there are none.

`Plan` (and `Apply` again) re-validates: action type, blocking risk flags,
`Guard.Resolve`, the path being a working-tree root, the date and operations
in progress. Dates are validated by git, never parsed by Brooom: empty,
dash-leading, control-character and letterless values are rejected
statically (`ErrInvalidDate`), the rest by a dry run (`git prune -n
--expire=<date>`), and the value is always passed as `--expire=<date>` so it
cannot be parsed as an option. A rebase, merge, cherry-pick, revert or bisect
in the repository or any linked worktree (`gitx.OperationInProgress` over
`gitx.WorktreeGitDirs`) skips all three actions, gc included because it
prunes too. Dry-run counts come from git (`prune -n` plus a bounded
`cat-file --batch-check` sample, `reflog expire --dry-run --verbose` "would
prune" lines, `count-objects -v` for gc); prune and reflog-expire skip with
"nothing to do" when git would change nothing. `Apply` measures
`count-objects` (size + size-pack + size-garbage) and, for gc and reflog
expiry, the fresh size of all reflog directories before and after; each part's
delta is clamped at 0 and the sum is `Entry.SizeBytes`. A running gc (git's
"already running"/`gc.pid` refusal) is a skip quoting git; other failures name
the repository.

Order and serialization: `actionPriority` runs reflog-expire, then prune, then
gc, so later steps see the expired reflog. The executor runs steps one after
another, so two maintenance steps never run concurrently on one repository; a
parallel executor would have to keep that per-repository serialization.

### Trash (`internal/trash`)

`Remove(path) (Record, error)` / `Restore(Record)`. Never follows symlinks.
Cross-device moves fall back to copy + verify + delete. Windows locked files
produce a clear error naming the file.

Windows Recycle Bin (`SHFileOperationW`, no cgo): the shell silently deletes
permanently what does not fit the bin or sits on a volume with the bin
disabled, so `Remove` refuses before calling it (never after) when
`NukeOnDelete=1`, the item is larger than `MaxCapacity`, the volume has no
GUID (network shares, some removable media) or the per-volume settings under
`HKCU\...\Explorer\BitBucket\Volume\<GUID>` cannot be read. A missing
`MaxCapacity` counts as unreadable; opening the Recycle Bin properties once
creates the key. Every refusal says that brooom leaves the item alone. The
shell API does not accept `\\?\` paths, so paths longer than 259 characters
are refused the same way. Pure logic (`$I` parsing, `pFrom` buffer, path
refusals, bin decision) is in `recyclebin_parse.go` and tested on every OS.
After the call the new `$I`/`$R` pair is recorded; a vanished item without a
matching pair is reported as a permanent deletion.

Descendants are re-rooted below `<volume>\$Recycle.Bin\<sid>\$R<random><ext>`
(about 70 characters), so `Remove` also measures the longest descendant path
during the size walk (`measureTree`) and `checkTreeDepth` refuses a tree whose
deepest item would exceed 259 characters, either already or once re-rooted
(the shell would stall on its permanent-deletion dialog). The shell call runs
in a goroutine bounded by the context and a timeout (`callBounded`); an
abandoned call cannot be cancelled, so the error reports the item as possibly
pending after one `Lstat` and nothing is retried. Restore validates
`StoredPath`/`InfoPath` against the bin directory derived from the original
path (`userBinDir`), which also covers volumes mounted into a folder. Parsed
`$I` files are cached per trasher, so a batch reads each once.

Cross-device moves (into a trash on another volume) check before copying:
`checkCopyable` refuses trees with entries `copyTree` cannot reproduce
(mount-point junctions on Windows, fifos and devices on unix) and, on Windows,
`procs.OpenFiles` refuses an item with open files. Directory symlinks are
recreated with the directory flag decided from the source link, and errors
that mean a locked file (access denied, sharing or lock violation) name the
file as in use. Restoring junctions is not supported.

The OS trash is selected per platform by `newOSTrasher` in
`ostrash_unix.go` (freedesktop), `ostrash_darwin.go` and
`ostrash_windows.go`, so the platform implementations never touch each
other's files. Records carry the strategy `trash`; manifests of earlier
releases may also hold `quarantine` or `delete`, which undo reports as not
restorable. Tests never reach the real trash: they use `testutil.DirTrasher`,
which the CLI tests install through the `newTrasher` seam.

### Sessions (`internal/session`)

One manifest per applied run, `~/.brooom/sessions/<id>.json` (mode 0600, dir
0700), ids like `20260929-224501-3f9a`. `Manifest` holds `version`, `id`,
`started_at`, `finished_at` (zero if the run crashed), `command`, `root` (the
repository or folder the run worked on; empty in older manifests), `entries[]`
and `reclaimed_bytes` (sum of `size_bytes` of `applied` entries only; call
`RecomputeReclaimed` after changing statuses). Each `Entry` records status
(`applied`, `failed`, `skipped`, `restored`), action, path, size, the trash
`Record` or `undo` data, `restorable` and a manual `recovery_hint`.

`Store.Save` writes atomically (temp file in the same dir, fsync, rename), so
a crash never leaves a half-written manifest; `*.tmp` files are ignored.
During a run the executor saves the snapshot once at the start and appends
one fsynced JSON line per entry to `<id>.journal` (`Store.AppendEntry`), so the
I/O of an apply is linear instead of rewriting the whole manifest per entry;
`Finish` saves the full snapshot, which removes the journal. `Load`/`List`
replay the journal on top of the snapshot (idempotent by entry index, a torn
last line is ignored). A manifest whose `id` differs from its file name
(`X.backup.json` holding id `X`) is refused, `List` reports it as a problem.
`Load` takes a full id or unique prefix (`ErrNotFound`, `ErrAmbiguous`; ids
with separators or `..` are refused). `List` returns manifests newest first
plus `[]Problem` for unreadable, corrupt or unsupported-version files, so one
damaged file never hides the history. `brooom sessions [--format json]` is
the read-only view: one row per session with id, root, applied items and
reclaimed bytes.

#### Undo

`action.PlanUndo` / `action.RunUndo` (`internal/action/undo.go`,
`undo_run.go`) hold the undo logic and `internal/cli/cmd_undo.go` only wires
it. Entries are planned and undone in reverse order; each is classified
`restore`, `conflict`, `cannot-restore`, `outside-scope` or `already-restored`.
A scope refusal is its own kind and summary bucket ("N skipped (outside scope;
re-run with --path)"), never "not restorable", because the data is intact.

Undo scope (#192, #287): the scope is the invocation's, never the manifest's:
the repository around the working directory, or `--path`. The undo hint
printed after an apply (`Result.UndoFlags`, from `app.scopeFlags`) repeats
`--path` and `--config` of the original invocation, so it works from any
directory. `session.Manifest.Workspaces` is only read: a session of an
earlier release that used `--workspaces` gets a note that `--path` is needed. Guard checks use
the entry's original paths (never trusted); the CLI builds the guard like
a sweep does (usage error outside a repository) and adds the user locations of
`detect.TargetSource` detectors only when an entry falls outside it. Conflicts
of actions that are not file conflicts (existing branch, occupied worktree
path) return an error matching `trash.ErrRestoreConflict`. The manifest is
saved after every restored entry.

#### macOS Trash and undo

On macOS items are trashed by calling `NSFileManager trashItemAtURL` directly
from Go without cgo (`github.com/ebitengine/purego` and its `objc` package, so
`CGO_ENABLED=0` cross builds keep working). Finder's "Put Back" works and
other volumes use their `.Trashes`; the resulting Trash path becomes
`Record.StoredPath`. The URL is built from the path string alone, so a symlink
is trashed as the link. If the native call fails for an item, the item is
moved into `~/.Trash` under a Finder-style unique name (`file 2.txt`); those
items have no Put Back metadata, and if `~/.Trash` is not writable the error
says to grant Full Disk Access or remove the item by hand (never a silent
permanent delete).

Since macOS 10.15 `~/.Trash` is protected by TCC: without Full Disk Access
the terminal gets `Operation not permitted` when it inspects or moves items
inside the Trash. Trashing works, but `brooom undo` may not be able to restore
them: it then reports "macOS denies access to the Trash; restore with Finder
'Put Back' or grant Full Disk Access to your terminal" and leaves the item
where it is. The macOS trasher keeps a private `RemoveMany` helper
(currently a loop over the per-item native call) that `Remove` shares; there is
no exported batch interface, the executor removes item by item.

The native call has no timeout of its own, so `callNative` runs it in a
goroutine under a deadline (`defaultNativeTimeout`, 2 minutes) and the caller's
context. A call that does not return in time yields `errNativePending`: the
item may still be moved later, so it is reported as an error without a
`Record`, is not retried through the `~/.Trash` fallback, and the remaining
items of the batch are skipped with an explanatory error.

### Performance memos and the scan cache

`catalog.Load` decodes and validates the embedded files once per process
(`sync.OnceValues`) and memoizes the resulting immutable `Catalog` per option
set (JSON key, bounded to 64 entries; failed loads are never memoized), so a
workspace scan pays for user extras only once. `catalog.BuildArtifacts` decodes
once and hands out copies; `buildartifacts` caches compiled rules per
`dirs`/`extra_dirs`.

`Executor.Plan` runs one `procs.OpenFiles` call for all trash targets
(`internal/action/openbatch.go`): targets that fail the static checks and
targets inside another target are left out, the result travels in the context
and `checkOpen` reads from it, with the single-path check as fallback. Without
a deadline the budget is `procs.Budget(n)` (3 s plus 50 ms per additional path,
at most 30 s).

`DirSize` with `Fresh: true` (every detector call, since ages must be exact)
neither reads nor writes the cache; it would otherwise rewrite every record on
each scan for a cache no later call could use. Only non-Fresh calls use it.

The `DirSize` cache file is never written when the marshalled document exceeds
`maxCacheBytes` (an existing file is removed; a cheap lower bound of the encoded
size skips the marshal for trees that are certainly too large) and not rewritten when no
directory was re-read or dropped (its mtime is refreshed instead, as the "last
used" stamp). `walk.PruneCache` deletes `dirsize-v1-*.json` files unused for 30
days, unreadable or oversized ones, those of vanished roots (`CheckRoots`) and
old temp files. The gitbloat blob caches (`gitbloat-blobs-<hash>.json`,
capped at 1 MiB, and their `.tmp` files) in the same directory are listed by age,
size and interrupted write; a cache hit refreshes the mtime. Their name is a hash
of the repository path, so there is no root check. It runs by age once per
process on the first cache write.

### Branch classification and delete-branch planning

`merged-branch` and `stale-branch` classify the branches of one repository with
a bounded worker pool (`detect.MapOrdered`, `detect.BranchWorkers`); workers
return their result and the detector consumes them in branch order, so output
does not depend on scheduling. On cached handles `gitx` answers "base has
nothing the branch lacks" for all refs with one batched
`for-each-ref --format=%(ahead-behind:<base>)` (git 2.41; older git falls back
to the per-branch queries), which skips the merge-base and rev-list processes
of the squash check for fresh branches. With the scan cache enabled
(`scan.cache`), squash/rebase verdicts are also stored in
`<cache>/verdicts/<key>.json`, keyed by the resolved base sha, tip sha, diff
flags and both safety caps plus a format version. Only definite answers are
stored, never truncated or failed checks; unreadable or inconsistent files are
misses; only scan handles (`Cache.SetVerdictDir`) read the store, so actions,
which use uncached handles, always verify against the live repository. Verdict
files are tiny but every new base commit orphans them, so the prune pass
(automatic once per process) ages them out like the size
caches (same max age, plus stale `tmp-*` files of interrupted writes); the
whole cache directory is safe to delete.

One `Executor.Plan` pass shares a `gitx.Cache` between its findings (carried in
the context, `action/plansnap.go`), so the branch listing, base branch,
worktrees and open pull requests are read once per repository instead of per
finding. The findings are planned by a few workers (`planWorkers`) and
consumed in finding order, so every action's `Plan` must be safe for
concurrent use. Apply and the re-plan that precedes it never get a snapshot:
they keep the live per-finding checks (a snapshot is deliberately not shared
between Plan and Apply). They share only `gitx.RunFacts`, created once per
apply run after confirmation: the git version, which base refs exist, the open
pull requests (one `gh` call per repository and run) and squash verdicts. The
verdicts live in a `gitx.Verdicts` store in memory that the plan pass of the
same executor filled; a verdict is a pure function of base and tip sha and
only this process writes the store, so the on-disk cache still never reaches
actions. In the apply pass delete-branch hands the decision of the re-plan to
`Apply` (`Step.live`) instead of evaluating twice in a row; the deletion stays
a compare-and-swap on the evaluated tip. The merged-branch detector asks `gh`
lazily, once the first merged branch needs the answer.

### Open files (`internal/procs`)

`procs.OpenFiles(ctx, paths)` reports which paths (or directories with an
open file below them) are open by a process. Best effort with a bounded
timeout (`DefaultTimeout` when the context has no deadline). `ErrUnavailable`
and `ErrIncomplete` (partial map still returned, `true` entries reliable) mean
unknown for `false` entries, never "safe". Per OS: `/proc/<pid>/fd` plus the
`cwd`, `root` and `exe` links on Linux (so a shell standing in a directory
counts; `(deleted)` targets and `/` are ignored), `lsof` on macOS (a process
whose working directory is exactly the directory counts), Restart Manager on
Windows. The Restart Manager only knows regular files: a shell or IDE whose
working directory is inside a directory holds just a directory handle and is
not detected there; `remove-worktree` moves the directory to the trash with
one rename, which fails cleanly on such a handle. `gitx.CwdWithin` additionally answers on every OS
whether this process's own working directory is inside a path.

`procs.Snapshot` (`detect.Env.Open`, set once per scan by the pipeline) lets
all detectors share one memoised listing of open files on macOS instead of
one lsof run per target: files are matched exactly, directories by prefix.
Elsewhere, or when the listing is unavailable, it delegates to `OpenFiles`.
It is a conservative pre-filter taken at the first query, so it can be stale on
a long scan; the executor's Plan-time check stays live and per target.

### Sizes and suggested commands

One sizing rule serves every number Brooom shows: allocated bytes
(`Stat_t.Blocks*512`, logical size on Windows) including the blocks of the
directories themselves, hard links counted once, symlinks as their own length
and never followed. `walk.DirSize` implements it for trees (cache version 5),
`walk.AllocatedSize` / `walk.LeafSize` for single items. The detectors
(`log-and-runtime-files`, `ai-artifacts`) size files with
`LeafSize`, so a sparse file counts what it occupies and `min_size_bytes` is
compared against that. `trash.treeSize` is `DirSize` too, and `Apply` passes the
size of its re-validating walk to the trasher through `trash.WithSizeHint`
(exact path only, accounting only; the Windows Recycle Bin trasher always
measures itself because its capacity check needs the live size). Plan,
manifest entry and "reclaimed" therefore agree. Every size is printed with
`output.FormatSize` (decimal SI); a test rejects other formatters.

Step descriptions carry no size: plans and prompts append the finding's size
once (`itemLine`), and omit it when it is 0 (branches, git maintenance).

`Finding.SuggestedAction.Command` and `Step.Command` are display-only, but they
are copied and pasted, so every value in them goes through
`findings.Quote` and names follow `--` (`git branch -d -- <name>`,
`trash -- <path>`). `findings.Quote` is the one OS-aware helper for detectors,
actions and the CLI hints: POSIX single quotes (`findings.ShellQuote`) on unix,
and on Windows a bare word when safe, double quotes when neither cmd.exe nor
PowerShell can act on the content (single quotes are a literal character in
cmd.exe), and PowerShell single quotes for values that need `$`, `%`, quotes or
a trailing backslash escaped. The bare set on Windows is only alphanumerics and
`/ . _ - : \` (',' and '@' are PowerShell syntax and get double quotes).
Values that need PowerShell single quotes (`%`, `"`, `$`, a trailing backslash)
are a PowerShell-only limitation: cmd.exe treats single quotes literally, so
such a hint parses correctly in PowerShell but not in cmd.exe; all other
values parse identically in both. `findings.QuoteFor(goos, s)` renders either
dialect on any OS for tests. The trash step's display command follows the host
shell (`internal/action/display.go`): POSIX on unix, PowerShell on Windows (the
Recycle Bin has no cmdlet, so that variant is a labelled, illustrative
comment).

`delete-branch` names the reference that justified `-d` ("fully merged into
upstream origin/x" or "HEAD"). Its recovery hint warns about unreachable objects
only when no branch, remote-tracking branch or tag still holds the deleted tip
(`for-each-ref --contains`); otherwise it names the ref that keeps the commits.

### Output (`internal/output`)

`Formatter.Write(w, *findings.Report, Options)`. `json` is the `Report` as
is; `ndjson` is one `Finding` per line; `plain` is paths only (for branches:
`<repo>\t<branch>`), one per line. Formats never write ANSI codes when
`Options.Color` is false. Formatters only ever write to stdout: the live
progress display lives on stderr, is never shown for the machine formats and
leaves the final results output unchanged (see "Live progress" below).

Every human-readable output (table, tree, summary, sessions, executor plans,
prompts and summaries, undo plans, error printing) passes untrusted text
(paths, refs, reasons, error messages) through `output.Sanitize`, which
replaces control runes with visible escapes (`\n`, `\x1b`, `\u2028`). Backslashes
are left alone so Windows paths stay readable. `json` and `ndjson` are machine
formats and are never altered, and neither is the `plain` output of findings
(paths and refs as they are). New human output must use it too.

The displayed shell command of a plan step (`Step.Command`) goes through
`output.Sanitize` as well: shell quoting keeps a command copy-pasteable but
does not neutralise control characters, so an ESC in a file name would still
reach the terminal.

### Live progress (`internal/progress`, `internal/cli/progressui`)

Long-running loops report to a `progress.Reporter` (`Phase`, `Step`, `Finding`,
`Reclaimed`, `Pause`), given as an option (`detect.RunOptions.Progress`,
`action.Options.Progress`, `action.UndoOptions.Progress`); nil means `Nop`, so
libraries have no TUI dependency and a reporter can never influence what a run
does. Phases: discover (scope resolution), scan (one step per target x detector
pair, one finding event per unique finding), plan, apply, undo.

The CLI decides once per invocation (`app.useProgress`, first call wins) with
the pure `showProgress`: the machine formats (json, ndjson, plain) never
draw; otherwise it needs stderr to be a terminal and neither `--quiet`, `CI`
(any value) nor `TERM=dumb`. The format is the one the command actually prints
(an applying run, `review` and `undo` print text whatever `output.format`
says). Until decided the
reporter is the no-op, so a path that forgets to decide fails closed.

`progressui.Display` keeps the run state under a mutex (reporter methods never
wait for the terminal) and renders it with a bubbletea program on stderr that
takes no input and installs no signal handler, so Ctrl-C keeps cancelling the
command context and prompts read stdin undisturbed. Terminal ownership is
strictly serialized: `Pause` stops the program and returns only once the
terminal is restored, and anything written to stdout (the scan report, the plan,
prompts, the apply summary) happens paused. The next `Phase` starts a fresh
program from the same state. `Stop` (deferred in `executeContext`, before any
error text is printed, so also after errors and Ctrl-C) erases the display after
a successful run, whose own output is the summary, and collapses it to one
`stopped` line after a failed one. All text shown from paths and labels goes
through `output.Sanitize`. Known cost of bubbletea v1: its package `init` asks an
interactive stdout terminal for its background colour once at process start
(skipped when stdout is not a terminal, and for `TERM=screen*`, `tmux*`, `dumb`).

### Completions and the CLI reference (`internal/cli`)

`completion.go` registers the dynamic shell completions (`--detector` with
comma lists, `--format`, the sweep preset argument, session ids, and
directories for the path argument and `--path`) by walking
the tree in `newRootCmd`; they only read registries,
config and manifests and degrade to an empty list. `completion_cmd.go` keeps
cobra's `completion` command visible with per-shell install instructions.
`docs/cli.md` is generated by `go run ./internal/tools/gendocs` from
`cli.NewRootCommand()` via `cli.ReferenceMarkdown`, and a test fails while the
committed file differs. Every visible command needs a `Short` and an `Example`
whose flags exist (also tested).

## Configuration

`~/.brooom/config.json` (override the home with `BROOOM_HOME`). Every field
has a default in `config.Default()`, so the file only contains overrides.
Layout of `~/.brooom`:

```
config.json
cache/        scan cache (safe to delete)
sessions/     <session-id>.json manifests
```

A repository may contain `.brooom.json` that can only tighten rules (disable
detectors, raise thresholds, add protected branches and excludes).

The full key reference, merge semantics, validation rules and the
`ForTarget`/exclude contract for detectors are in [config.md](config.md).

## Conventions

- Go 1.24, `CGO_ENABLED=0`, no runtime dependencies. Keep third-party
  dependencies minimal (cobra, golang.org/x/sys, golang.org/x/term, and the
  Charm libraries bubbletea, bubbles and lipgloss, imported only by
  `internal/cli/progressui`).
- Cross-platform: use `filepath`, never hard-code `/`; OS-specific code in
  `_windows.go` / `_darwin.go` / `_unix.go` files with build tags as needed;
  every package must build for linux, darwin and windows on amd64 and arm64.
- Tests: table-driven, `t.TempDir()`, `testutil.NewRepo` for git. Tests must
  pass on Linux, macOS and Windows (CI runs all three). Never touch the real
  home directory: set `BROOOM_HOME` / `HOME` / `XDG_DATA_HOME` to temp dirs.
- No test may reach the real OS trash: actions get `testutil.DirTrasher`,
  and the CLI tests swap `newTrasher` for it in `TestMain`.
- Errors: wrap with context (`fmt.Errorf("...: %w", err)`), name the path.
- Comments explain *why*; doc comments on every exported identifier.
- Functions stay below cyclomatic complexity 15 (`gocyclo`).
- `go vet`, `golangci-lint run` and `go test ./...` must pass before a PR.

## Exit codes

`0` success, `1` error, `2` usage error (bad flags, unknown subcommands, wrong
argument counts, `brooom help <unknown>`, flags a command ignores such as
`undo -f` / `undo -d`; every command in the tree rejects extra arguments, pinned
by a test that walks the tree), `3` no target was scanned because of scan
errors (for example every repository was skipped), `4` the scan ran and its
report was written, but a detector failed on a target.

Scan errors come in two classes (#191), told apart by `findings.ScanError.Fatal`
(`"fatal": true` in the json report, omitted otherwise):

- Fatal: a detector returned a plain error, or panicked, so its findings for
  that target are missing and the report may be incomplete. A sweep that only
  reports (a machine format) exits `4` and says `N detector failure(s)` on
  stderr.
- Notes: something was skipped or only partly checked and the result is still
  trustworthy: a repository refused for dubious ownership, a repository or
  root skipped before scanning (bad per-repo config, path outside the scope),
  an interrupted scan, or an error a detector wraps with `detect.Note` (for
  example git-bloat's incomplete large-blob scan). Notes keep exit `0`.

A detector picks the class by what it returns from `Detect`: `detect.Note(err)`
for a note, any other error for a failure. The engine sets `Fatal` from that
and nowhere else. Exit `3` still wins when nothing was scanned at all. The
acting flows (`sweep` with a plan, `review`) keep their own exit rules and do
not return `4`: a run that has already trashed
items must not report a failure for a detector problem that its plan never saw;
the errors are still printed (stderr or in-band). Every error is listed in the
report (table, tree, summary, json) or on stderr (plain, ndjson).

An explicit `-f tree|table|summary` renders the scan report before the plan
of an acting run; without `-f` an acting run shows only the plan; a machine
format only reports and is a usage error with `--yes`.

Decision (#182 item 5): the issue proposed rejecting every explicit `--format`
on an acting run. Brooom instead honours an explicit human format
(`tree`, `table`, `summary`), because a user who asked for a report expects to
see it before confirming, and rejects only machine formats, whose consumers
would receive a plan prompt mixed into their data.
