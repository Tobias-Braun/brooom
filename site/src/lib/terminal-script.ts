/**
 * Content and timing of the landing page terminal demo, kept as data so the
 * Vue component contains no scattered timers. Nothing here is recorded from
 * the real binary: the output is hand-authored, modelled on the sweep flow in
 * internal/action/prompt.go (renderPlan: a "detector / action: N items, SIZE"
 * header per group, each item with its "$ command" line, then "total
 * reclaimable"; per-group confirmation prompts; renderSummary with session
 * and undo hint) and internal/action/executor.go (the lowercase dry run
 * notice). Only the default safe preset is shown, so only detectors that
 * belong to it appear. Every size and path is sample data inside an
 * imaginary demo repository.
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

export const SWEEP_COMMAND = 'brooom sweep';
export const APPLY_COMMAND = 'brooom sweep --apply';

/**
 * The plan `brooom sweep` prints (renderPlan): a header per detector and
 * action, then each item's description with its display-only command.
 */
const planLines: Line[] = [
  bold('merged-branch / delete-branch: 2 items, 0 B'),
  plain('  delete branch feat/login-form with -d (0 B)'),
  dim('    $ git branch -d feat/login-form'),
  plain('  delete branch fix/typo with -d (0 B)'),
  dim('    $ git branch -d fix/typo'),
  bold('build-artifacts / trash: 1 item, 610.0 MB'),
  plain('  move node_modules (610.0 MB) to trash (610.0 MB)'),
  dim('    $ trash /home/dev/code/acme-api/node_modules'),
  plain('total reclaimable: 610.0 MB'),
];

/** The dry run: the plan, then the notice that nothing was changed. */
export const dryRunLines: Line[] = [
  ...planLines,
  plain('dry run: nothing was changed; re-run with --apply to execute'),
];

/** The confirmation prompts of `--apply`, each auto-answered "y". */
const confirmLines: Line[] = [
  plain('merged-branch / delete-branch: apply 2 items (0 B)? [y]es/[n]o/[i]ndividually/[q]uit y'),
  plain('build-artifacts / trash: apply 1 item (610.0 MB)? [y]es/[n]o/[i]ndividually/[q]uit y'),
];

/** renderSummary; the real flow prints no per-action progress lines. */
const summaryLines: Line[] = [
  plain('summary: 3 applied, 0 skipped, 0 failed'),
  ok('reclaimed: 610.0 MB'),
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
  { kind: 'print', lines: planLines, delay: 55 },
  { kind: 'print', lines: confirmLines, delay: 450 },
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
