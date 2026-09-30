# Findings schema

Findings are the contract between detectors, output formats, actions and
future clients (dashboard, agent). `brooom scan --format json` prints a
`Report`; `--format ndjson` prints one `Finding` per line; `brooom clean
--from <file>` reads a `Report` back.

The schema is versioned by `schema_version` (currently **1**). Fields may be
added without a version bump; removing or renaming fields, or changing their
meaning, bumps the version. Go types: [`internal/findings`](../internal/findings).

A machine-readable [JSON Schema](findings.schema.json) (draft 2020-12) describes
the `Report`. It is kept in sync with the Go types by a reflection test
(`internal/output/schema_test.go`): every JSON field must be documented in the
schema and `required` must match the fields without `omitempty`. Objects stay
open (`additionalProperties`) on purpose, so reports from newer producers with
added fields still validate.

## Output formats

`--format` selects how a report is rendered:

| Format | Content |
| --- | --- |
| `table` | Human-readable, grouped by detector. |
| `tree` | Findings in their directory structure, one tree per scope. |
| `json` | The full `Report`; arrays are never `null`. |
| `ndjson` | One compact `Finding` per line, streamable; errors are not part of it. |
| `plain` | Paths only, one per line, for `xargs`. |
| `summary` | Counts and reclaimable bytes per detector. |

`plain` is deliberately conservative because its output is piped into other
tools. It lists only actionable findings of kind `file`, `dir`, `worktree` and
`branch` (branches as `<repo path>TAB<branch>`). Flagged findings, the git
maintenance kinds and `worktree-missing` are omitted, and findings whose path
contains a line break are skipped with an error; use `--format json` for
everything. Worktree paths are listed for inspection only: removing them with
`rm` leaves git metadata behind (run `git worktree prune` or use
`brooom worktrees --apply`).

## Finding

| Field | Type | Description |
| --- | --- | --- |
| `id` | string | Deterministic 16-hex-char ID from detector, kind, path and ref. Stable across runs. |
| `detector` | string | Detector name, e.g. `merged-branch`. |
| `scope` | object | `{type, path}`; `type` is `repo`, `root` or `user`. |
| `path` | string | Absolute, symlink-resolved path. For branches: the repository root. |
| `kind` | string | `file`, `dir`, `branch`, `worktree`, `worktree-missing`, `git-loose-objects`, `git-packs`, `git-reflog`, `git-large-blob`. |
| `ref` | string? | Branch name for `branch` findings, branch of a `worktree`. |
| `tool` | string? | Tool that produced the clutter (`claude-code`, `npm`, ...). |
| `size_bytes` | int | Bytes freed by acting on the finding (estimate for git maintenance). |
| `last_modified` | RFC 3339? | Newest relevant mtime or last commit time. |
| `age_days` | int | Whole days between `last_modified` and the scan. |
| `confidence` | string | `high`, `medium`, `low`. |
| `evidence` | array | `{code, message, value?}` reasons; `code` is stable snake_case. |
| `suggested_action` | object | `{type, args?, command?, reason?}`; `type` is `none`, `trash`, `delete-branch`, `remove-worktree`, `prune-worktrees`, `git-gc`, `git-prune`, `git-reflog-expire`. |
| `risk_flags` | array | See below. |
| `meta` | object? | Detector-specific string key/values. |

`suggested_action.type == "none"` means *flagged, not suggested*.

## Risk flags

Blocking flags force `suggested_action.type = "none"`; actions refuse to act
on them without `--force`. `--force` never overrides `file_open_by_process`,
`worktree_locked`, `worktree_operation_in_progress`, `current_branch` and `protected_branch`
(`findings.Actionable(flags, force)`).

| Flag | Blocking | Meaning |
| --- | --- | --- |
| `unpushed_commits` | yes | Branch has commits on no remote. |
| `file_open_by_process` | yes | A process has the file open (e.g. log still written) or stands in the directory (Linux: cwd, root, exe; macOS: cwd and maps; not detected on Windows, whose Restart Manager sees only files, so `remove-worktree` probes with a rename there; a worktree containing brooom's own current directory is flagged the same way). |
| `worktree_dirty` | yes | Worktree has uncommitted changes. |
| `worktree_locked` | yes | Worktree is locked. |
| `worktree_operation_in_progress` | yes | A rebase, merge, cherry-pick, revert or bisect is in progress in the worktree. |
| `has_open_pr` | yes | An open pull request uses the branch (via `gh`). |
| `current_branch` | yes | Branch is checked out in some worktree. |
| `protected_branch` | yes | Branch matches a protected pattern. |
| `tracked_files` | yes | Path contains files tracked by git. |
| `recently_modified` | no | Changed within `thresholds.recent_days`. Informational only: for `worktrees` it never withholds the removal or lowers the confidence, because worktrees left by an agent run must be removable right away (evidence `recently_modified`). |
| `gitignored` | no | Path is ignored by git. |
| `never_pushed` | no | Branch was never pushed. |
| `upstream_gone` | no | Upstream tracking branch was deleted. |
| `symlink` | no | Path is a symlink; only the link is removed. |
| `outside_repo` | no | User-level location outside any repository. |

## Report

| Field | Description |
| --- | --- |
| `schema_version` | Schema version (1). |
| `brooom_version` | Version of the binary that produced the report. |
| `generated_at` | Scan time (UTC). |
| `scopes` | Scanned repos/roots/user locations. |
| `findings` | Sorted by detector, path, ref. Never `null`. |
| `totals` | `{findings, actionable, reclaimable_bytes, by_detector}`; nested paths are counted once. |
| `errors` | Non-fatal problems `{detector?, path?, message}`. |

## Example

The example below is kept in
[`internal/findings/testdata/example-report.json`](../internal/findings/testdata/example-report.json)
and verified by `TestExampleReportRoundTrip`.

```json
{
  "schema_version": 1,
  "brooom_version": "0.1.0",
  "generated_at": "2026-09-29T20:15:00Z",
  "scopes": [{ "type": "repo", "path": "/home/dev/src/shop" }],
  "findings": [
    {
      "id": "a3c9e1b27f604d8e",
      "detector": "merged-branch",
      "scope": { "type": "repo", "path": "/home/dev/src/shop" },
      "path": "/home/dev/src/shop",
      "kind": "branch",
      "ref": "feat/checkout-v2",
      "size_bytes": 0,
      "last_modified": "2026-08-11T16:40:02Z",
      "age_days": 49,
      "confidence": "high",
      "evidence": [
        {
          "code": "squash_merged_into",
          "message": "all commits are in origin/main (squash merge detected via patch-id)",
          "value": "origin/main"
        }
      ],
      "suggested_action": {
        "type": "delete-branch",
        "command": "git branch -D feat/checkout-v2",
        "reason": "squash-merged into origin/main; -D is required because git cannot see the squash merge"
      },
      "risk_flags": ["upstream_gone"],
      "meta": { "tip": "9f2c41d0b8e7a6c5d4e3f2a1b0c9d8e7f6a5b4c3" }
    }
  ],
  "totals": {
    "findings": 1,
    "actionable": 1,
    "reclaimable_bytes": 0,
    "by_detector": {
      "merged-branch": { "findings": 1, "actionable": 1, "reclaimable_bytes": 0 }
    }
  }
}
```
