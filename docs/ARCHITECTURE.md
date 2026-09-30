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
   branches are deleted with `git branch -d`; `-D` is used only when base ancestry, or remote containment (alone or together with a squash/rebase merge), is re-verified at apply time, or with `--force` (a squash/rebase merge of commits on no remote needs `--force`); worktrees are
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
| `internal/gitx` | Read-only-safe git runner (`gitx.Env`: repository-selecting `GIT_*` variables such as `GIT_DIR`/`GIT_INDEX_FILE` and inherited `GIT_CONFIG_*` are stripped, `GIT_OPTIONAL_LOCKS=0`, `GIT_NO_LAZY_FETCH=1` (git >= 2.44), `core.fsmonitor=false`, C locale, no prompts, a 10 minute default timeout for contexts without a deadline, also for `Pipe`/`PipeLimit`, 6 hours for maintenance via `gitx.WithTimeout`; a passed bound is a `*TimeoutError` that matches `context.DeadlineExceeded`; only promisor/lazy-fetch errors in `MergedInto` mean not merged, other object errors such as corruption are returned; `gh` runs with the same sanitized environment) and git helpers behind a per-repo `Repo` handle (branches and upstreams, base detection, merge detection incl. squash/rebase via patch-id, remote containment, worktrees, dirty check, open PRs via `gh`), `Pipe`/`PipeLimit` (stream one git command into another without buffering, for history scans; `PipeLimit` and `ExecRunner.MaxOutput` kill the process(es) past a byte cap and return `ErrOutputLimit`; squash detection streams `log -p`/`diff` into `patch-id` this way, capped at 64 MiB, and rebase detection never matches a branch containing merge commits, only the squash net-diff check can) and `Repo.Memo` (per-repo memoization of expensive measurements); `Cache` shares memoized handles per scan (`detect.Env.Repos`), uncached `Open` is for actions. On cached handles ancestry is answered for all branches by one `for-each-ref --merged`, patch ids are computed once per commit, and one scan-wide breaker stops calling `gh` after its first timeout or network failure, also after earlier successes; every external command has a `WaitDelay` so a grandchild holding a pipe cannot outlive a deadline. |
| `internal/findings` | **The findings schema** (see [findings.md](findings.md)): `Finding`, `Report`, IDs, risk flags, totals. Stable contract. |
| `internal/detect` | `Detector` interface, registry, `Env`, parallel `Run` engine. |
| `internal/detectors/<name>` | One package per detector, self-registering via `init()`. `internal/detectors/all` blank-imports them. |
| `internal/catalog` | Embedded JSON data: AI tool locations, dev tool log/cache locations, build artifact dirs + project markers. Extensible via config. |
| `internal/presets` | Sweep presets as pure data (detector set, minimum confidence, config overlay) plus `Apply` (deep copy, never mutates the loaded config). Presets never touch safety settings. |
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

A detector that needs user-level targets (catalog locations below the home
directory) additionally implements the optional `detect.TargetSource`
(`ExtraTargets(ctx, cfg) ([]scope.Target, error)`). The scan pipeline calls it
once per scan for every selected, globally enabled detector, appends the
`TargetUser` targets (deduplicated by declaring detector, tool and resolved
path, so tools sharing a base directory are all scanned; missing locations dropped) and allows
their paths in the guard. It only declares locations and never detects.

The `ai-artifacts` detector (`internal/detectors/aiartifacts`) matches the
`ai` category of the catalog. Project targets get one pruned `walk.Walk`
(`.git`, `scan.skip_dirs`, well-known huge dirs, matched directories, root and
repo excludes and nested repositories are not descended into). Catalog
`protect` patterns always win: a candidate that is protected, below a
protected path or a directory containing one is dropped, as is a matched
directory containing a `.git` entry at any depth (`walk.DirSummary.HasVCS`,
gathered by the fresh size pass). User-level targets exist only when
`detectors.ai-artifacts.user_locations` is true; findings there are entries
inside a location, never the location itself. `tracked_files` is the only
blocking flag `--force` lifts; an open file keeps the action at `none`.

The `log-and-runtime-files` detector (`internal/detectors/logs`) mirrors
`ai-artifacts` for the catalog categories `logs`, `cache`, `os-junk` and
`crash` (toggled by `detectors.log-and-runtime-files.categories`; user-level
targets need `detectors.log-and-runtime-files.user_locations`, set for one run
by `brooom logs --user`). The walk, protect and nested-repository rules are the
same (the code is duplicated locally on purpose; extracting a shared helper is
a later cleanup). Differences: the size pass is `Fresh` because logs are written
in place; a recently modified `*.log` / `*.log.N` file drops from high to medium
confidence; and one batched `procs.OpenFiles` per target flags files a process
has open with `file_open_by_process`, which is blocking and never overridable,
so those findings suggest `none` even with `--force`. `tracked_files` is
force-overridable and then suggests `trash` with a `forced: ...` reason.

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
configured `trash.Trasher`. `Plan` re-validates in this order and skips with a
reason at the first failure:

1. `Guard.ResolveParent` (the final element is kept, so symlinks are removed
   as links and never followed); outside the allowed roots is refused.
2. Static refusals on the resolved path: filesystem/volume roots, allowed
   roots, repository roots, VCS metadata (`.git`, `.hg`, `.jj`, `.svn`, by
   `walk.IsVCSName`) or anything inside it, the Brooom home
   (and anything containing it) and its `sessions` and `quarantine` dirs, and
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
7. Delete-strategy guard: with the `delete` strategy a finding whose
   `Meta["user_data_risk"]` is `untracked` is refused, and so is any path for
   which git cannot show right now (`ls-files --others --exclude-standard`)
   that it holds no untracked, non-ignored file. Meta is only a conservative
   extra signal, since a findings file can drop it.
8. Catalog protection (`protect.go`): the catalog protect rules (`.env`,
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
with `RefuseByIdentity`, which compares file identity (`os.SameFile`) of the
path and its ancestors with `.git`, the Brooom home, the user's home and the
sessions/quarantine dirs; `brooom clean --from` vetting runs it too. Step 3
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
The identity check fails closed: an entry that exists but whose identity cannot
be read counts as an alias and refuses the removal; only a missing entry is
"not the same". On Windows the identity comes from `CreateFile` plus
`GetFileInformationByHandle` (`fileid_windows.go`) rather than the lazy
`os.SameFile`, which answers false on any error.

The `delete` strategy is refused outside a git repository and whenever git
cannot answer the untracked-files check (step 7 and `Apply`); it never falls
back to allowing the removal.

Resolving the trasher in `Plan` has no side effects. The one-time delete
warning (and its `.delete-warned` marker in the Brooom home) is emitted by
`Env.BeforeDelete`. The executor calls it when an applied plan contains a
trash or worktree-removal group whose trasher uses the delete strategy, before
the confirmation prompt (`Executor.warnBeforeDelete`), so the user reads it
before deciding; the trash action calls it again right before a removal as a
backstop (it warns at most once per run). Dry runs never consume it.

Not overridable by `--force`: steps 1, 2, 3, 4 (open files, also via the
`file_open_by_process` flag), 7 and 8. `--force` only lifts blocking risk flags
and the tracked-files check. `Apply` re-resolves and re-checks the static
refusals, then re-runs the nested `.git`, open-file, tracked-files and
delete-strategy checks against live state (a step may come from any caller, so
neither its `Meta` nor its risk flags are trusted; the delete strategy needs
git to show that the path holds no untracked, non-ignored file, and is refused
outside a repository). The nested `.git` check walks the tree a second time.
`Apply` removes the re-resolved path and returns a manifest entry with the
trash record; a path that vanished is a skip, not a failure. `Undo` restores
with the strategy recorded in the entry (`Env.TrasherFor`), never the
configured one, after checking that the original path lies inside the allowed
roots and is neither inside `.git` nor inside the Brooom home (config, cache,
sessions, quarantine). That check is by name, then by file identity of every
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

Known limitation (deferred from #88, plan item 4): a local branch whose name
collides with a tag or a remote-tracking name (for example a local branch
called `origin/main`) is handled safely (detectors and actions address refs
fully qualified) but is not reported as an informational finding yet. Before deleting,
`branch.<name>.remote/merge` are recorded in `Entry.Undo`
(`upstream_remote`, `upstream_merge`). Undo validates them (name shape, git's
`check-ref-format`), and only when it created the branch restores them with
`git branch --set-upstream-to` if the remote-tracking ref exists, else by
writing the two config keys.

Dubious ownership: `gitx.Open` returns `ErrNotRepo` only for git's "not a git
repository"; a repository git refuses because another user owns it is an
`*gitx.UnsafeRepoError` (`errors.Is(err, gitx.ErrUnsafeRepo)`) carrying git's
message and the `safe.directory` hint, and other failures keep their stderr.
`detect.Run` reports it once per repository as "skipped: dubious ownership ...",
`git purge` and the worktree and branch actions report it as a visible skip.

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
candidate modified within `thresholds.recent_days` (fresh mtimes) gets
`recently_modified` and one lower confidence level, which keeps it out of the
`safe` preset (high only). The directory always goes through the configured trasher
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

Scope of a run from a linked worktree: the guard allows that worktree only.
The main worktree is registered with `Guard.WithRepoMeta` and is accepted by
`Guard.ResolveRepoMeta` as that exact directory, never anything below it
(sibling worktrees, files). `ResolveRepoMeta` is used only to locate the
repository: the branch and worktree detectors (`merged-branch`, `stale-branch`,
`worktrees`), `delete-branch`, the worktree actions and `clean --from` vetting
of a git finding's repository. Git maintenance (`git-bloat`, `git-gc` and
friends) uses `Resolve`; `brooom git purge` from a linked worktree runs in the
linked worktree itself (same shared repository). A linked worktree the guard
does not allow (a sibling below main, or one outside the repository, which is
git's default for `git worktree add ../x`) is never examined or offered;
`worktrees` reports each existing one as an informational finding (action
`none`, low confidence, evidence code `outside_scope`, message carrying
`scope.OutsideWorktreeHint`: "outside the allowed scope; run `brooom roots add
<parent>` or use --workspaces"), so it is visible without `--verbose` in every
format that lists non-actionable findings (`plain` stays a path pipe of
actionable findings only). `Guard.OutsideNote` gives the hint only for a
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

#### `brooom clean --from` (`internal/cli/cmd_clean.go`, `clean_scope.go`, `clean_vet.go`)

A findings file is untrusted input. `findings.ReadReport` only checks the
envelope (size cap 256 MiB, one JSON value, `schema_version` 1..current;
unknown fields are tolerated). Everything else comes from the invocation: the
scope is rebuilt like a scan (`buildTargets`: the repository around the working
directory, or the configured roots with `--workspaces`/`--root`), never from the
report's `scopes` or a finding's `scope` (a note in `--verbose` only).

Three guards are built: `project` (repo or roots), `user` (the
`detect.TargetSource` locations, only with `--user`) and their union, which the
actions receive. A finding claiming `scope.type` `user` must resolve in `user`,
all others in `project`, so a finding cannot pick the wider guard. Trash
findings resolve with `ResolveParent`, all others with `Resolve`; a trash
finding whose path is a symlink now, without the `symlink` risk flag, is refused
(directory swapped for a link after the scan). Git findings (by kind or action)
also need their repository (`Path`, or `Meta["repo"]` for worktree findings) to
be a repository of the scope, and a `Ref` that neither starts with `-` nor
contains control characters. Refused findings are listed and counted and make
the command exit 1 after the accepted ones were processed; findings selected
away with `--id` are never evaluated. Findings without an action (also with
`--force`) are skipped with a re-scan hint, an unknown action type is refused,
a known but unimplemented one is skipped. Risk flags, sizes and ages from the
file are not trusted: the executor's `Plan` re-validates everything.

The user's selection and configuration apply as in a scan. `-d/--detector` is
validated against the registry (unknown name: exit 2) and findings of other
detectors are left alone silently. `vet` derives the effective configuration
(`ForTarget`, including the tighten-only `.brooom.json`) from the resolved
path, never from the file's scope, and refuses a finding of a detector that is
disabled there or whose path lies below an `exclude`d directory (`clean_config.go`).
The disabled-detector part is a courtesy check only: it is keyed on the
`Detector` field of the file, so a renamed detector bypasses it (pinned by
`TestCleanDisabledDetectorCheckIsCourtesy`). Deriving the detector from the
finding kind would not help, since several detectors share a kind. The exclude
check, the scope guard, the catalog protection and the action re-validation do
not read that field.
The catalog protect rules are enforced by the trash action (step 8 above).
Accepted git maintenance findings lose their `Args`: the expiry of `git-gc`,
`git-prune` and `git-reflog-expire` comes from the repository's configuration
(`targetGitBloat`), so a forged `{"expire": "now"}` has no effect. `delete-branch`
derives merged, squash-merged and remote containment from the repository at
plan and apply time and never reads `Detector` or `Args["verified"]`. Execution
is `runExecutor`, shared with the shortcut commands.

### Sweep presets (`internal/presets`, `internal/cli/cmd_sweep.go`)

`brooom sweep` resolves the preset (flag, then `sweep.preset`, then `safe`) and
calls `runCleanup` with the preset's detectors, its `MinConfidence` and an
overlay. The overlay is applied right after `config.Load` in `newScanRequest`
and before `ForTarget`, so root overrides and the tighten-only `.brooom.json`
still act on top of it. Findings below the confidence floor are dropped in
`execute`, before reporting and planning; blocked findings are not treated
specially and stay blocked. Age thresholds are set as `min(current, preset)`
and lowered values live in one table (`presets.AggressiveAges`). The aggressive git
expiries follow the same rule (`shorterExpiry`): the preset's `90.days.ago`
replaces a configured `reflog_expire` / `prune_expire` only when it is shorter
in the restricted comparison of `now`, `never`, `N.days.ago` and `N.weeks.ago`;
longer, equal and unparseable values are kept, so the default `2.weeks.ago`
prune expiry is never raised. `standard` leaves the log
categories as configured (it never switches one on, so a category the user
disabled stays disabled; only `safe` limits them to OS junk and old logs).
Overlays never
touch `RecentDays`, protected branches, the trash strategy or `AllowDelete`,
and never switch `ai-artifacts.user_locations` on. `--detector` is intersected
with the preset; naming one outside it is a usage error. Preset detectors that
are not linked into the build are skipped with a verbose note
(`cleanupSelection.skipUnavailable`), explicitly requested ones are an error.
`config.PresetNames` mirrors `presets.Names()` (pinned by a test) because
`presets` imports `config`.

#### The git maintenance actions

`internal/action/gitmaint*.go` holds `git-gc` (`git gc --quiet --prune=<date>`),
`git-prune` (`git prune --expire=<date>`) and `git-reflog-expire`
(`git -c gc.refs/stash.reflogExpire=never -c gc.refs/stash.reflogExpireUnreachable=never
-c gc.reflogExpire=<date> reflog expire --all`, see `gitx.ReflogExpireArgs`). They destroy data that is
otherwise recoverable, so each is opt-in (`brooom git purge --gc|--prune|
--reflog-expire`), never restorable (`Restorable=false`, `Undo` returns an
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

`brooom git purge` without flags only reports the git-bloat findings.
`--gc` acts on repositories with a loose-object or pack finding only;
`--reflog-expire`/`--prune` synthesize a finding for every repository in scope
(one per common git dir, linked worktrees folded into the main one) and let
the dry run decide. Machine formats are refused together with the flags, and
invalid dates are usage errors before anything is planned.

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
creates the key. All refusals recommend `--trash-strategy quarantine`. The
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

Cross-device moves (quarantine on another volume) check before copying:
`checkCopyable` refuses trees with entries `copyTree` cannot reproduce
(mount-point junctions on Windows, fifos and devices on unix) and, on Windows,
`procs.OpenFiles` refuses an item with open files. Directory symlinks are
recreated with the directory flag decided from the source link, and errors
that mean a locked file (access denied, sharing or lock violation) name the
file as in use. Restoring junctions is not supported.

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

#### Undo, purge and the retention notice

`action.PlanUndo` / `action.RunUndo` (`internal/action/undo.go`,
`undo_run.go`) hold the undo logic and `internal/cli/cmd_undo.go` only wires
it. Entries are planned and undone in reverse order; each is classified
`restore`, `conflict`, `cannot-restore`, `outside-scope` or `already-restored`.
A scope refusal is its own kind and summary bucket ("N skipped (outside scope;
re-run with -w)"), never "not restorable", because the data is intact.

Undo scope (#192): `session.Manifest.Workspaces` records that the run used
`--workspaces`. `brooom undo` of such a session resolves the workspace scope
without the flag (`adoptSessionScope`); only that fact is taken from the
manifest, the guard is still built from the configured roots and every entry
path is still checked against it. The undo hint printed after an apply
(`Result.UndoFlags`, from `app.scopeFlags`) repeats `--workspaces`, `--root`
and `--config` of the original invocation. Guard checks use
the entry's original paths (never trusted); the CLI builds the guard like
`scan` does (usage error outside a repository) and adds the user locations of
`detect.TargetSource` detectors only when an entry falls outside it. Conflicts
of actions that are not file conflicts (existing branch, occupied worktree
path) return an error matching `trash.ErrRestoreConflict`. The manifest is
saved after every restored entry.

`trash.ListQuarantine` / `trash.Purge` (`internal/trash/purge.go`) list
session directories (names must match the session id format, symlinks are
skipped and reported, manifest.json gives time and size with the directory
mtime and a recursive size as fallback) and delete them;
`session.Store.MarkPurged` makes the affected manifest entries
non-restorable. `retentionNotice` (`internal/cli/notice.go`) is a root
post-run hook that prints the one-line stderr notice.

#### macOS Trash and undo

On macOS items are trashed by calling `NSFileManager trashItemAtURL` directly
from Go without cgo (`github.com/ebitengine/purego` and its `objc` package, so
`CGO_ENABLED=0` cross builds keep working). Finder's "Put Back" works and
other volumes use their `.Trashes`; the resulting Trash path becomes
`Record.StoredPath`. The URL is built from the path string alone, so a symlink
is trashed as the link. If the native call fails for an item, the item is
moved into `~/.Trash` under a Finder-style unique name (`file 2.txt`); those
items have no Put Back metadata, and if `~/.Trash` is not writable the error
suggests `--trash-strategy quarantine` (never a silent permanent delete).

Since macOS 10.15 `~/.Trash` is protected by TCC: without Full Disk Access
the terminal gets `Operation not permitted` when it inspects or moves items
inside the Trash. Trashing works, but `brooom undo` may not be able to restore
them: it then reports "macOS denies access to the Trash; restore with Finder
'Put Back' or grant Full Disk Access to your terminal" and leaves the item
where it is. The trasher also implements the optional `trash.BatchTrasher`
(`RemoveMany`, currently a loop over the per-item native call).

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

The `DirSize` cache file is never written when the marshalled document exceeds
`maxCacheBytes` (an existing file is removed; a cheap lower bound of the encoded
size skips the marshal for trees that are certainly too large) and not rewritten when no
directory was re-read or dropped (its mtime is refreshed instead, as the "last
used" stamp). `walk.PruneCache` deletes `dirsize-v1-*.json` files unused for 30
days, unreadable or oversized ones, those of vanished roots (`CheckRoots`) and
old temp files. It runs by age once per process on the first cache write and in
full via `brooom purge`.

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
(automatic once per process, and `brooom purge`) ages them out like the size
caches (same max age, plus stale `tmp-*` files of interrupted writes); the
whole cache directory is safe to delete.

One `Executor.Plan` pass shares a `gitx.Cache` between its findings (carried in
the context, `action/plansnap.go`), so the branch listing, base branch,
worktrees and open pull requests are read once per repository instead of per
finding. Apply and the re-plan that precedes it never get a snapshot: they keep
the live per-finding checks (a snapshot is deliberately not shared between
Plan and Apply).

### Open files (`internal/procs`)

`procs.OpenFiles(ctx, paths)` reports which paths (or directories with an
open file below them) are open by a process. Best effort with a bounded
timeout (`DefaultTimeout` when the context has no deadline). `ErrUnavailable`
and `ErrIncomplete` (partial map still returned, `true` entries reliable) mean
unknown for `false` entries, never "safe". Per OS: `/proc/<pid>/fd` plus the
`cwd`, `root` and `exe` links on Linux (so a shell standing in a directory
counts; `(deleted)` targets and `/` are ignored), `lsof` on macOS, Restart
Manager on Windows. `gitx.CwdWithin` additionally answers on every OS whether
this process's own working directory is inside a path.

### Sizes and suggested commands

One sizing rule serves every number Brooom shows: allocated bytes
(`Stat_t.Blocks*512`, logical size on Windows) including the blocks of the
directories themselves, hard links counted once, symlinks as their own length
and never followed. `walk.DirSize` implements it for trees (cache version 5),
`walk.AllocatedSize` / `walk.LeafSize` for single items. The detectors
(`large-untracked`, `log-and-runtime-files`, `ai-artifacts`) size files with
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
comment). A quarantine move shows the real destination pattern
`<quarantine>/<session-id>/<n>/`, since the session id only exists once the run
starts.

`delete-branch` names the reference that justified `-d` ("fully merged into
upstream origin/x" or "HEAD"). Its recovery hint warns about unreachable objects
only when no branch, remote-tracking branch or tag still holds the deleted tip
(`for-each-ref --contains`); otherwise it names the ref that keeps the commits.

### Output (`internal/output`)

`Formatter.Write(w, *findings.Report, Options)`. `json` is the `Report` as
is; `ndjson` is one `Finding` per line; `plain` is paths only (for branches:
`<repo>\t<branch>`), one per line. Formats never write ANSI codes when
`Options.Color` is false.

Every human-readable output (table, tree, summary, sessions, executor plans,
prompts and summaries, undo plans, error printing) passes untrusted text
(paths, refs, reasons, error messages) through `output.Sanitize`, which
replaces control runes with visible escapes (`\n`, `\x1b`, `\u2028`). Backslashes
are left alone so Windows paths stay readable. `json` and `ndjson` are machine
formats and are never altered, and neither is the `plain` output of findings
(paths and refs as they are). `roots list -f plain` is the one exception: it
sanitizes each path, because a newline inside a root path would otherwise forge
a second entry in a format that is one path per line. New human output must
use it too.

The displayed shell command of a plan step (`Step.Command`) goes through
`output.Sanitize` as well: shell quoting keeps a command copy-pasteable but
does not neutralise control characters, so an ESC in a file name would still
reach the terminal.

### Completions and the CLI reference (`internal/cli`)

`completion.go` registers the dynamic shell completions (`--detector` with
comma lists, `--format`, `--preset`, `--trash-strategy`, `--root`, session ids,
`roots remove`) by walking the tree in `newRootCmd`; they only read registries,
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

`0` success, `1` error, `2` usage error (bad flags, unknown subcommands, wrong
argument counts, `brooom help <unknown>`, flags a command ignores such as
`undo -f` / `undo -d`; every command in the tree rejects extra arguments, pinned
by a test that walks the tree), `3` no target was scanned because of scan
errors (for example every repository was skipped). A partial scan failure stays
`0`, and so does a scan in which a detector fails inside a scanned scope: the
engine records every error a detector returns as a scan error, including
non-fatal notes (a linked worktree outside the scope), so "every detector
reported an error" cannot tell a failure from a note. A detector that fails
inside a scanned scope is reported but stays `0`; its errors are in the report
(table, tree, summary, json) or on stderr (plain, ndjson).

Suggested apply commands (`applyHint`) repeat the invocation without `--apply`,
`--yes`/`-y` (a pasted hint must not skip the confirmation) and
`--format`/`-f` (an explicit machine format is refused with `--apply`). A
`scan --force` hint keeps `--force` on every command it names. With `--apply`,
an explicit `-f tree|table|summary` renders the scan report before the plan;
without `-f` an applying run prints no report; a machine format is a usage
error with `--apply`.

Decision (#182 item 5): the issue proposed rejecting every explicit `--format`
together with `--apply`. Brooom instead honours an explicit human format
(`tree`, `table`, `summary`), because a user who asked for a report expects to
see it before confirming, and rejects only machine formats, whose consumers
would receive a plan prompt mixed into their data. Fatal detector errors versus
non-fatal notes in the exit code are tracked in #191.
