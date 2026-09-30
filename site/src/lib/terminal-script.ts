/**
 * Content and timing of the landing page terminal demo, kept as data so the
 * Vue component contains no scattered timers. Nothing here is recorded from
 * the real binary: the output is hand-authored, modelled on the sweep flow in
 * internal/action/prompt.go (renderPlan: a "detector / action: N items, SIZE"
 * header per group, each item with its "$ command" line, then "total
 * reclaimable", then the one confirmation question) and
 * internal/action/brief.go (renderBriefSummary, the output after the yes).
 * Only detectors of the default everything preset appear. Every size and path
 * is sample data inside an imaginary demo repository.
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

/**
 * The typed command uses br, the short name the install scripts add next to
 * brooom. The output below keeps saying brooom because the binary prints its
 * full name.
 */
export const SWEEP_COMMAND = 'br sweep';

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

/** The one question sweep asks (confirm in prompt.go), answered with y. */
export const questionLine: Line = bold('Proceed with 3 items (610.0 MB)? [y/N] y');

/**
 * renderBriefSummary: after the yes, sweep prints the undo line, then the
 * counts and the reclaimed size as the last line.
 */
const summaryLines: Line[] = [
  plain('undo: brooom undo 20260930-142201-3f9a'),
  ok('2 merged branches removed, 1 build artifact removed. 610.0 MB reclaimed'),
];

/** The complete animation: sweep, plan, question, summary, pause, loop. */
export const terminalScript: Step[] = [
  { kind: 'type', text: SWEEP_COMMAND },
  { kind: 'print', lines: planLines, delay: 55 },
  { kind: 'pause', ms: 1800 },
  { kind: 'print', lines: [questionLine], delay: 0 },
  { kind: 'print', lines: summaryLines, delay: 450 },
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
 * reduced-motion visitors see, so it must show the finished sweep.
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
 * gets the whole run in one static block.
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
