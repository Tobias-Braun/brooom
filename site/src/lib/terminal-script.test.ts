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
const REAL_DETECTORS = ['merged-branch', 'build-artifacts'];
const REAL_ACTIONS = ['delete-branch', 'trash'];

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
    expect(all).toContain('dry run: nothing was changed; re-run with --apply to execute');
    expect(all).not.toContain('Dry run:');
    for (const d of REAL_DETECTORS) expect(all).toContain(d);
    // Group headers of renderPlan: "detector / action: N items, SIZE".
    const headers = [...all.matchAll(/^(\S+) \/ (\S+): \d+ items?, /gm)];
    // Two groups, shown once in the dry run and once in the apply run.
    expect(headers).toHaveLength(4);
    for (const h of headers) {
      expect(REAL_DETECTORS).toContain(h[1]);
      expect(REAL_ACTIONS).toContain(h[2]);
    }
    expect(all).toContain('total reclaimable: 610.0 MB');
  });

  it('only shows detectors of the default safe preset', () => {
    expect(transcript()).not.toContain('ai-artifacts');
  });

  it('prints no invented per-action progress lines', () => {
    const lines = transcript().split('\n');
    // Commands only ever appear as indented "$ ..." lines inside the plan.
    expect(lines.filter((l) => /^(git |trash )/.test(l))).toEqual([]);
    expect(lines.filter((l) => l.startsWith('    $ ')).length).toBeGreaterThan(0);
  });

  it('never touches anything outside the demo repository', () => {
    const commands = transcript()
      .split('\n')
      .filter((l) => l.startsWith('    $ ') && !l.includes('git branch'));
    expect(commands.length).toBeGreaterThan(0);
    for (const l of commands) expect(l).toContain('/acme-api/');
  });

  it('uses git branch -d, never a forced delete', () => {
    expect(transcript()).not.toMatch(/branch -D|--force/);
  });

  it('transcript includes the cleared dry run, the final frame does not', () => {
    expect(transcript()).toContain('dry run: nothing was changed');
    expect(finalFrame().map((l) => l.text).join('\n')).not.toContain('dry run: nothing was changed');
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
