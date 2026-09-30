/**
 * Content and timing of the landing page terminal demo, kept as data so the
 * Vue component contains no scattered timers. Nothing here is recorded from
 * the real binary: the output is hand-authored, modelled on the table format
 * (internal/output/table.go: SIZE AGE CONF ACTION PATH columns, one block per
 * detector, totals per block and overall) and on the apply flow
 * (internal/action/prompt.go: per-group confirmation, summary, undo hint).
 * It only uses real detector names, actions and flags from docs/SPEC.md, and
 * every size and path is sample data inside an imaginary demo repository.
 */

/** How a rendered line is coloured; purely presentational. */
export type Tone = 'plain' | 'cmd' | 'bold' | 'dim' | 'ok';

export interface Line {
  text: string;
  tone: Tone;
}

/** Content and timing of the animation, as a flat list of steps. */
export type Step =
  | { kind: 'type'; text: string; speed?: number }
  | { kind: 'print'; lines: Line[]; delay?: number }
  | { kind: 'pause'; ms: number }
  | { kind: 'clear' };

/**
 * What the runner applies to the screen when a tick fires. Expanding steps
 * to actions up front keeps the runner ignorant of typing and printing.
 */
export type Action =
  | { op: 'begin-command' }
  | { op: 'char'; ch: string }
  | { op: 'line'; line: Line }
  | { op: 'clear' }
  | { op: 'noop' };

/** Wait `delay` ms, then apply `action`. */
export interface Tick {
  delay: number;
  action: Action;
}

export const DEFAULT_TYPE_SPEED_MS = 55;
export const DEFAULT_PRINT_DELAY_MS = 70;
/** Pause before typing starts, so the first key press is not instant. */
const BEFORE_TYPING_MS = 350;

const plain = (text: string): Line => ({ text, tone: 'plain' });
const dim = (text: string): Line => ({ text, tone: 'dim' });
const bold = (text: string): Line => ({ text, tone: 'bold' });
const ok = (text: string): Line => ({ text, tone: 'ok' });
const blank = plain('');

export const SWEEP_COMMAND = 'brooom sweep';
export const APPLY_COMMAND = 'brooom sweep --apply';

/** The dry-run table printed by `brooom sweep`. */
export const dryRunLines: Line[] = [
  bold('merged-branch - branches already merged into the base branch (2)'),
  dim('SIZE  AGE  CONF  ACTION         PATH  REF'),
  plain('   -  3mo  high  delete-branch  .     feat/login-form'),
  plain('   -  2mo  high  delete-branch  .     fix/typo'),
  dim('2 findings, 2 actionable, 0 B reclaimable'),
  blank,
  bold('worktrees - leftover git worktrees (1)'),
  dim('    SIZE  AGE  CONF  ACTION           PATH'),
  plain('212.0 MB  5w   high  remove-worktree  ../acme-api-agent-3'),
  dim('1 finding, 1 actionable, 212.0 MB reclaimable'),
  blank,
  bold('ai-artifacts - agent transcripts, logs and caches (2)'),
  dim('   SIZE  AGE  CONF    ACTION  PATH'),
  plain('1.1 GB  6w   high    trash   .claude/projects/run-0142.jsonl'),
  plain('96.0 MB  3mo  medium  trash   .aider.chat.history.md'),
  dim('2 findings, 2 actionable, 1.2 GB reclaimable'),
  blank,
  bold('build-artifacts - rebuildable build output (1)'),
  dim('    SIZE  AGE  CONF  ACTION  PATH'),
  plain('610.0 MB  4mo  high  trash   node_modules'),
  dim('1 finding, 1 actionable, 610.0 MB reclaimable'),
  blank,
  bold('6 findings, 6 actionable, 2.0 GB reclaimable'),
  blank,
  plain('Dry run: nothing was changed; re-run with --apply to execute'),
];

/** The confirmation prompts of `--apply`, each auto-answered "y". */
const confirmLines: Line[] = [
  plain('merged-branch / delete-branch: apply 2 items (0 B)? [y]es/[n]o/[i]ndividually/[q]uit y'),
  plain('worktrees / remove-worktree: apply 1 item (212.0 MB)? [y]es/[n]o/[i]ndividually/[q]uit y'),
  plain('ai-artifacts / trash: apply 2 items (1.2 GB)? [y]es/[n]o/[i]ndividually/[q]uit y'),
  plain('build-artifacts / trash: apply 1 item (610.0 MB)? [y]es/[n]o/[i]ndividually/[q]uit y'),
];

/** Per-action progress; only ever touches the demo repository. */
const progressLines: Line[] = [
  plain('git branch -d feat/login-form  (~/code/acme-api)'),
  plain('git branch -d fix/typo  (~/code/acme-api)'),
  plain('git worktree remove ../acme-api-agent-3'),
  plain('trash ~/code/acme-api/.claude/projects/run-0142.jsonl'),
  plain('trash ~/code/acme-api/.aider.chat.history.md'),
  plain('trash ~/code/acme-api/node_modules'),
];

const summaryLines: Line[] = [
  blank,
  plain('summary: 6 applied, 0 skipped, 0 failed'),
  ok('reclaimed: 2.0 GB'),
  plain('session: 20260930-142201-3f9a'),
  plain('undo: brooom undo 20260930-142201-3f9a'),
];

/** The complete animation: dry run, pause, apply, summary, pause, loop. */
export const terminalScript: Step[] = [
  { kind: 'type', text: SWEEP_COMMAND },
  { kind: 'print', lines: dryRunLines, delay: 55 },
  { kind: 'pause', ms: 3500 },
  { kind: 'clear' },
  { kind: 'type', text: APPLY_COMMAND },
  { kind: 'print', lines: confirmLines, delay: 450 },
  { kind: 'print', lines: progressLines, delay: 320 },
  { kind: 'print', lines: summaryLines, delay: 120 },
  { kind: 'pause', ms: 4500 },
];

/** Flattens steps into timed actions for the runner. */
export function expandSteps(steps: readonly Step[]): Tick[] {
  const ticks: Tick[] = [];
  for (const step of steps) {
    switch (step.kind) {
      case 'type':
        ticks.push({ delay: BEFORE_TYPING_MS, action: { op: 'begin-command' } });
        for (const ch of step.text) {
          ticks.push({ delay: step.speed ?? DEFAULT_TYPE_SPEED_MS, action: { op: 'char', ch } });
        }
        break;
      case 'print':
        for (const line of step.lines) {
          ticks.push({ delay: step.delay ?? DEFAULT_PRINT_DELAY_MS, action: { op: 'line', line } });
        }
        break;
      case 'pause':
        ticks.push({ delay: step.ms, action: { op: 'noop' } });
        break;
      case 'clear':
        ticks.push({ delay: 0, action: { op: 'clear' } });
        break;
    }
  }
  return ticks;
}

/** Applies one action to the screen without mutating the previous state. */
export function applyAction(screen: readonly Line[], action: Action): Line[] {
  switch (action.op) {
    case 'begin-command':
      return [...screen, { text: '$ ', tone: 'cmd' }];
    case 'char': {
      const last = screen[screen.length - 1];
      if (!last) return [{ text: action.ch, tone: 'cmd' }];
      return [...screen.slice(0, -1), { ...last, text: last.text + action.ch }];
    }
    case 'line':
      return [...screen, action.line];
    case 'clear':
      return [];
    case 'noop':
      return [...screen];
  }
}

/** The screen once every tick has been applied, in order. */
export function renderTicks(ticks: readonly Tick[]): Line[] {
  return ticks.reduce<Line[]>((screen, t) => applyAction(screen, t.action), []);
}

/**
 * The completed last frame. It is what server rendering, no-JS viewers and
 * reduced-motion visitors see, so it must show the finished apply run.
 */
export function finalFrame(steps: readonly Step[] = terminalScript): Line[] {
  return renderTicks(expandSteps(steps));
}

/**
 * The tallest the screen ever gets, in lines. The terminal box reserves this
 * height up front so the page never shifts while output is printed.
 */
export function maxScreenLines(steps: readonly Step[] = terminalScript): number {
  let screen: Line[] = [];
  let max = 0;
  for (const t of expandSteps(steps)) {
    screen = applyAction(screen, t.action);
    max = Math.max(max, screen.length);
  }
  return max;
}

/**
 * Every line the animation ever shows, including what a `clear` step wipes
 * from the screen. Used for the screen reader alternative so assistive tech
 * gets the dry run as well as the apply run, in one static block.
 */
export function transcript(steps: readonly Step[] = terminalScript): string {
  const out: string[] = [];
  let screen: Line[] = [];
  for (const t of expandSteps(steps)) {
    if (t.action.op === 'clear') {
      out.push(...screen.map((l) => l.text), '');
      screen = [];
    } else {
      screen = applyAction(screen, t.action);
    }
  }
  out.push(...screen.map((l) => l.text));
  return out.join('\n');
}
