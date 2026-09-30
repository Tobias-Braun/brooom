import { describe, expect, it } from 'vitest';
import {
  SWEEP_COMMAND,
  expandSteps,
  finalFrame,
  maxScreenLines,
  questionLine,
  terminalScript,
  transcript,
  type Step,
} from './terminal-script';

/** Detector names and actions that exist in docs/SPEC.md and the Go code. */
const REAL_DETECTORS = ['merged-branch', 'build-artifacts'];
const REAL_ACTIONS = ['delete-branch', 'trash'];

describe('terminal script', () => {
  it('types the real command once', () => {
    const typed = terminalScript.filter((s) => s.kind === 'type').map((s) => (s as { text: string }).text);
    expect(typed).toEqual([SWEEP_COMMAND]);
  });

  it('shows the plan, then one question answered with y, then the summary', () => {
    const text = finalFrame().map((l) => l.text);
    expect(text[0]).toBe(`$ ${SWEEP_COMMAND}`);
    const total = text.indexOf('total reclaimable: 610.0 MB');
    const question = text.indexOf(questionLine.text);
    expect(total).toBeGreaterThan(0);
    expect(question).toBe(total + 1);
    expect(text.filter((l) => l.includes('[y/N]'))).toHaveLength(1);
    expect(text.some((l) => l.startsWith('undo: brooom undo '))).toBe(true);
    expect(text[text.length - 1]).toMatch(/^\d+ .+\. 610\.0 MB reclaimed$/);
  });

  it('shows only real detectors and actions', () => {
    const all = transcript();
    expect(all).not.toContain('dry run');
    for (const d of REAL_DETECTORS) expect(all).toContain(d);
    // Group headers of renderPlan: "detector / action: N items, SIZE".
    const headers = [...all.matchAll(/^(\S+) \/ (\S+): \d+ items?, /gm)];
    expect(headers).toHaveLength(2);
    for (const h of headers) {
      expect(REAL_DETECTORS).toContain(h[1]);
      expect(REAL_ACTIONS).toContain(h[2]);
    }
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
