import { describe, expect, it } from 'vitest';
import {
  APPLY_COMMAND,
  SWEEP_COMMAND,
  expandSteps,
  finalFrame,
  maxScreenLines,
  terminalScript,
  transcript,
  type Step,
} from './terminal-script';

/** Detector names and actions that exist in docs/SPEC.md and the Go code. */
const REAL_DETECTORS = ['merged-branch', 'worktrees', 'ai-artifacts', 'build-artifacts'];
const REAL_ACTIONS = ['delete-branch', 'remove-worktree', 'trash'];

describe('terminal script', () => {
  it('types the real commands, dry run first', () => {
    const typed = terminalScript.filter((s) => s.kind === 'type').map((s) => (s as { text: string }).text);
    expect(typed).toEqual([SWEEP_COMMAND, APPLY_COMMAND]);
  });

  it('ends on the finished apply run with summary and undo hint', () => {
    const text = finalFrame().map((l) => l.text);
    expect(text[0]).toBe(`$ ${APPLY_COMMAND}`);
    expect(text.some((l) => l.startsWith('reclaimed: '))).toBe(true);
    expect(text.some((l) => l.startsWith('undo: brooom undo '))).toBe(true);
  });

  it('shows the dry-run notice and only real detectors and actions', () => {
    const all = transcript();
    expect(all).toContain('Dry run: nothing was changed');
    for (const d of REAL_DETECTORS) expect(all).toContain(d);
    // Table rows: SIZE ("-" or "1.1 GB"), AGE, CONF, then the ACTION column.
    const rows = /^ *(?:-|[\d.]+ (?:B|KB|MB|GB)) +\S+ +\S+ +(\S+) /gm;
    const actions = [...all.matchAll(rows)].map((m) => m[1]);
    expect(actions).toHaveLength(6);
    for (const a of actions) expect(REAL_ACTIONS).toContain(a);
  });

  it('never touches anything outside the demo repository', () => {
    const progress = transcript()
      .split('\n')
      .filter((l) => /^(git |trash )/.test(l));
    expect(progress.length).toBeGreaterThan(0);
    for (const l of progress) expect(l).toMatch(/acme-api|\.\.\/acme-api/);
  });

  it('uses git branch -d, never a forced delete', () => {
    expect(transcript()).not.toMatch(/branch -D|--force/);
  });

  it('transcript includes the cleared dry run, the final frame does not', () => {
    expect(transcript()).toContain('6 findings, 6 actionable, 2.0 GB reclaimable');
    expect(finalFrame().map((l) => l.text).join('\n')).not.toContain('6 findings, 6 actionable');
  });

  it('reserves at least the height of the tallest screen', () => {
    expect(maxScreenLines()).toBeGreaterThanOrEqual(finalFrame().length);
  });
});

describe('step expansion', () => {
  const steps: Step[] = [
    { kind: 'type', text: 'ab', speed: 5 },
    { kind: 'print', lines: [{ text: 'x', tone: 'plain' }], delay: 7 },
    { kind: 'pause', ms: 100 },
    { kind: 'clear' },
  ];

  it('turns steps into timed actions', () => {
    const ticks = expandSteps(steps);
    expect(ticks.map((t) => t.action.op)).toEqual(['begin-command', 'char', 'char', 'line', 'noop', 'clear']);
    expect(ticks.filter((t) => t.action.op === 'char').every((t) => t.delay === 5)).toBe(true);
    expect(ticks.find((t) => t.action.op === 'line')?.delay).toBe(7);
  });

  it('types into the command line and clear empties the screen', () => {
    expect(finalFrame(steps.slice(0, 2)).map((l) => l.text)).toEqual(['$ ab', 'x']);
    expect(finalFrame(steps)).toEqual([]);
  });
});
