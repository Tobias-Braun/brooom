/**
 * The detectors as data, condensed from "Detectors (v1)" in docs/SPEC.md and
 * the defaults in docs/config.md. Names are the config keys and --detector
 * values from internal/config/detectors.go; which presets run each one comes
 * from presets.ts, so it cannot disagree with the presets section.
 */

export interface Detector {
  name: string;
  family: 'Git' | 'Files';
  body: string;
}

export const detectors: Detector[] = [
  {
    name: 'merged-branch',
    family: 'Git',
    body: 'Local branches whose tip is already in the base branch (main, master, develop, trunk), found by ancestry and, by default, by patch id so squash merges count too. Protected branches such as main and release/* are never suggested.',
  },
  {
    name: 'worktrees',
    family: 'Git',
    body: 'Linked worktrees whose branch is merged, detached worktrees whose commits all landed on the base under other ids, and worktrees whose directory is gone. Worktrees in use are protected, and dirty ones are left to br review.',
  },
  {
    name: 'git-bloat',
    family: 'Git',
    body: 'Many loose objects, a large reflog, too many packs or large blobs. Suggests git gc, reflog expiry and pruning with configurable expiry dates; the stash reflog is never expired.',
  },
  {
    name: 'stale-branch',
    family: 'Git',
    body: 'Local branches without a commit for 90 days that are not merged. Each one shows whether its commits exist on a remote and, through gh when available, whether a pull request is open. This is unmerged work, so br review decides on it one by one.',
  },
  {
    name: 'ai-artifacts',
    family: 'Files',
    body: 'Run logs, JSONL transcripts, caches and scratch folders of Claude Code, Cursor, Aider, Copilot and others in the project, plus the transcripts Claude Code keeps for the repository in your home directory. Settings, instructions, skills and commands are never touched.',
  },
  {
    name: 'log-and-runtime-files',
    family: 'Files',
    body: 'npm, yarn and pnpm debug logs, rotated logs, crash dumps, editor swap files, .DS_Store and Thumbs.db, Jest, Vitest and pytest caches and coverage output. Files a process has open are flagged, not suggested.',
  },
  {
    name: 'build-artifacts',
    family: 'Files',
    body: 'node_modules, dist, build, target, .venv, __pycache__, .next and similar folders, weighted by how long the project has been inactive (30 days by default, from the last commit and source change).',
  },
];
