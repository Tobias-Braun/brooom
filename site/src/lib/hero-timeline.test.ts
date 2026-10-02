import { describe, expect, it } from 'vitest';
import {
  COMMAND,
  FLICK_S,
  LOOP_S,
  TIMELINE,
  captionAt,
  formatSize,
  makePlan,
  question,
  reclaimed,
  sceneAt,
  seeded,
  staticPlan,
  staticScene,
  transcript,
} from './hero-timeline';

const plans = [1, 2, 3, 4, 5].flatMap((seed) => [makePlan(seeded(seed), false), makePlan(seeded(seed), true)]);
const ticks = Array.from({ length: 901 }, (_, i) => (i / 900) * LOOP_S);

describe('hero timeline', () => {
  it('splits the loop into contiguous phases', () => {
    const phases = Object.values(TIMELINE);
    expect(phases[0]![0]).toBe(0);
    for (let i = 1; i < phases.length; i++) expect(phases[i]![0]).toBe(phases[i - 1]![1]);
    expect(LOOP_S).toBe(9);
  });

  it('sends three robots with 2-3 items each, two with fewer on compact screens', () => {
    for (let seed = 1; seed <= 20; seed++) {
      const wide = makePlan(seeded(seed), false);
      expect(wide.robots).toHaveLength(3);
      expect(wide.items.length).toBeGreaterThanOrEqual(6);
      expect(wide.items.length).toBeLessThanOrEqual(9);
      const compact = makePlan(seeded(seed), true);
      expect(compact.robots).toHaveLength(2);
      expect(compact.items.length).toBeGreaterThanOrEqual(2);
      expect(compact.items.length).toBeLessThanOrEqual(4);
    }
  });

  it('varies the junk between loops', () => {
    const kinds = new Set(plans.map((p) => p.items.map((it) => it.kind).join()));
    expect(kinds.size).toBeGreaterThan(1);
  });

  it('drops every item while the robots walk and flicks it during the sweep', () => {
    for (const plan of plans) {
      for (const it of plan.items) {
        expect(it.drop).toBeGreaterThanOrEqual(TIMELINE.robots[0]);
        expect(it.drop).toBeLessThan(TIMELINE.robots[1]);
        expect(it.flick).toBeGreaterThanOrEqual(TIMELINE.sweep[0]);
        expect(it.flick + FLICK_S).toBeLessThanOrEqual(LOOP_S);
      }
      expect(sceneAt(TIMELINE.type[0], plan).robots).toEqual([]);
      expect(sceneAt(TIMELINE.type[0], plan).items).toHaveLength(plan.items.length);
      expect(sceneAt(TIMELINE.ask[1] - 0.01, plan).sweeper).toBeNull();
      expect(sceneAt(LOOP_S - 0.01, plan).items).toEqual([]);
    }
  });

  it('keeps the reduced-motion junk on the left half', () => {
    for (const plan of [staticPlan(), ...plans]) {
      const scene = staticScene(plan);
      expect(scene.items.every((it) => it.u < 0.5)).toBe(true);
      expect(scene.robots).toEqual([]);
    }
  });
});

describe('hero caption', () => {
  it('formats sizes like output.FormatSize', () => {
    expect(formatSize(0)).toBe('0 B');
    expect(formatSize(999)).toBe('999 B');
    expect(formatSize(1000)).toBe('1.0 kB');
    expect(formatSize(999_950)).toBe('1.0 MB');
    expect(formatSize(610_000_000)).toBe('610.0 MB');
    expect(formatSize(4_249_000_000)).toBe('4.2 GB');
  });

  it('asks the real sweep question about the junk in the scene', () => {
    for (const plan of plans) {
      const q = question(plan);
      expect(q).toMatch(/^Proceed with \d+ items? \(\d+\.\d [kMG]B\)\? \[y\/N\] $/);
      expect(q).toContain(`${plan.items.length} item`);
      expect(q).toContain(`(${formatSize(plan.total)})`);
    }
  });

  it('starts at an empty prompt, types the command, waits at the question, answers y and counts up', () => {
    const plan = plans[0]!;
    expect(captionAt(0, plan)).toEqual({ lines: ['$ '], cursor: 0, opacity: 1 });
    expect(captionAt(TIMELINE.ask[0], plan)).toEqual({ lines: [`$ ${COMMAND}`, question(plan)], cursor: 1, opacity: 1 });
    expect(captionAt(TIMELINE.ask[1] - 0.01, plan).lines[1]).toBe(question(plan));
    expect(captionAt(TIMELINE.ask[1], plan).lines[1]).toBe(`${question(plan)}y`);
    const end = captionAt(TIMELINE.exit[0], plan);
    expect(end.lines).toEqual(transcript(plan));
    expect(end.cursor).toBe(-1);
    expect(captionAt(LOOP_S, plan).opacity).toBe(0);
  });

  it('only counts up, and reaches the total before the sweeper exits', () => {
    for (const plan of plans) {
      let before = 0;
      for (const t of ticks) {
        const now = reclaimed(t, plan);
        expect(now).toBeGreaterThanOrEqual(before);
        before = now;
      }
      expect(reclaimed(TIMELINE.exit[0], plan)).toBe(plan.total);
    }
  });

  it('shows no wording the CLI does not print', () => {
    for (const plan of plans) {
      for (const t of ticks) {
        for (const line of captionAt(t, plan).lines) {
          expect(line).toMatch(/^(\$ .*|Proceed with .*|reclaimed (\d+ B|\d+\.\d [kMGT]B))$/);
          expect(line).not.toMatch(/freed|✓/);
        }
      }
    }
  });
});
