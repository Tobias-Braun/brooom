/**
 * The sweep presets as data, copied from all() in internal/presets/presets.go.
 * internal/presets/site_test.go fails when a name, detector, confidence floor
 * or `includes` line here no longer matches the Go definition, so the page
 * shows exactly what `br sweep <preset>` does.
 */

export type Confidence = 'low' | 'medium' | 'high';

export interface Preset {
  id: string;
  summary: string;
  /** Run by plain `br sweep` when the config sets no sweep.preset. */
  isDefault: boolean;
  detectors: string[];
  minConfidence: Confidence;
  /** Per-detector floors above minConfidence. */
  floors: Record<string, Confidence>;
  includes: string[];
}

export const presets: Preset[] = [
  {
    id: 'after-agents',
    summary: 'clean up after an agent run: merged worktrees and branches, agent leftovers',
    isDefault: false,
    detectors: ['ai-artifacts', 'merged-branch', 'worktrees'],
    minConfidence: 'medium',
    floors: {},
    includes: [
      'worktrees whose branch is merged (squash and rebase merges too), clean ones only',
      'local branches merged into the base branch',
      'AI tool artifacts in the repository: run logs, transcripts, caches, scratch files',
    ],
  },
  {
    id: 'tidy',
    summary: 'low-risk hygiene: logs, OS junk, test caches and coverage output',
    isDefault: false,
    detectors: ['log-and-runtime-files'],
    minConfidence: 'medium',
    floors: {},
    includes: [
      'debug and rotated logs, crash dumps, editor swap files',
      '.DS_Store, Thumbs.db and other OS junk',
      'test caches and coverage output',
    ],
  },
  {
    id: 'everything',
    summary: 'all of the above plus build artifacts of inactive projects and git maintenance',
    isDefault: true,
    detectors: ['ai-artifacts', 'build-artifacts', 'git-bloat', 'log-and-runtime-files', 'merged-branch', 'worktrees'],
    minConfidence: 'medium',
    floors: { 'build-artifacts': 'high' },
    includes: [
      'everything in after-agents and tidy',
      'build artifacts (node_modules, target, .venv, ...) of inactive projects only',
      'git gc, reflog expiry and pruning; expiries longer than 90.days.ago are shortened to it, shorter ones are kept',
    ],
  },
];

/** Optional flags of `br sweep`, shown in brackets after every preset's command. */
export const sweepFlags = [
  { flag: '--dry-run', meaning: 'only shows the plan and changes nothing' },
  { flag: '--yes', meaning: 'skips the question, for scripts' },
];

/** Names of the presets that run the detector, narrowest first. */
export function presetsRunning(detector: string): string[] {
  return presets.filter((p) => p.detectors.includes(detector)).map((p) => p.id);
}
