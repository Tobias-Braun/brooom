import { describe, expect, it } from 'vitest';
import { createRunner, type Timers } from './runner';
import type { Tick } from './terminal-script';

/** A manual clock: timers only fire when the test advances time. */
function fakeClock() {
  let now = 0;
  let nextId = 1;
  const pending = new Map<number, { at: number; fn: () => void }>();
  const timers: Timers = {
    setTimeout: (fn, ms) => {
      const id = nextId++;
      pending.set(id, { at: now + ms, fn });
      return id;
    },
    clearTimeout: (id) => {
      pending.delete(id as number);
    },
  };
  const advance = (ms: number) => {
    const end = now + ms;
    for (;;) {
      const due = [...pending.entries()].filter(([, t]) => t.at <= end).sort((a, b) => a[1].at - b[1].at)[0];
      if (!due) break;
      pending.delete(due[0]);
      now = due[1].at;
      due[1].fn();
    }
    now = end;
  };
  return { timers, advance, pendingCount: () => pending.size };
}

const ticks: Tick[] = [
  { delay: 10, action: { op: 'char', ch: 'a' } },
  { delay: 10, action: { op: 'char', ch: 'b' } },
  { delay: 100, action: { op: 'noop' } },
];

describe('createRunner', () => {
  it('fires ticks after their own delay and advances', () => {
    const clock = fakeClock();
    const seen: number[] = [];
    const r = createRunner({ ticks, timers: clock.timers, onTick: (_t, i) => seen.push(i) });
    r.start();
    clock.advance(9);
    expect(seen).toEqual([]);
    clock.advance(1);
    expect(seen).toEqual([0]);
    clock.advance(10);
    expect(seen).toEqual([0, 1]);
  });

  it('pauses without losing the position and resumes from it', () => {
    const clock = fakeClock();
    const seen: number[] = [];
    const r = createRunner({ ticks, timers: clock.timers, onTick: (_t, i) => seen.push(i) });
    r.start();
    clock.advance(10);
    r.pause();
    expect(r.state).toBe('paused');
    expect(clock.pendingCount()).toBe(0);
    clock.advance(10_000);
    expect(seen).toEqual([0]);
    r.resume();
    expect(r.state).toBe('running');
    clock.advance(10);
    expect(seen).toEqual([0, 1]);
  });

  it('ignores pause and resume in the wrong state', () => {
    const clock = fakeClock();
    const r = createRunner({ ticks, timers: clock.timers, onTick: () => {} });
    r.pause();
    r.resume();
    expect(r.state).toBe('idle');
    expect(clock.pendingCount()).toBe(0);
  });

  it('stop clears every timer and cannot be resumed', () => {
    const clock = fakeClock();
    const seen: number[] = [];
    const r = createRunner({ ticks, timers: clock.timers, onTick: (_t, i) => seen.push(i) });
    r.start();
    r.stop();
    expect(clock.pendingCount()).toBe(0);
    r.resume();
    clock.advance(10_000);
    expect(seen).toEqual([]);
    expect(r.state).toBe('stopped');
  });

  it('loops and reports each restart', () => {
    const clock = fakeClock();
    let loops = 0;
    const seen: number[] = [];
    const r = createRunner({
      ticks,
      timers: clock.timers,
      onTick: (_t, i) => seen.push(i),
      onLoop: () => loops++,
    });
    r.start();
    clock.advance(120 + 10);
    expect(seen).toEqual([0, 1, 2, 0]);
    expect(loops).toBe(1);
  });

  it('stops after the last tick when looping is off', () => {
    const clock = fakeClock();
    const r = createRunner({ ticks, timers: clock.timers, onTick: () => {}, loop: false });
    r.start();
    clock.advance(1000);
    expect(r.state).toBe('stopped');
    expect(clock.pendingCount()).toBe(0);
  });

  it('restarting from scratch never leaves a second timer behind', () => {
    const clock = fakeClock();
    const r = createRunner({ ticks, timers: clock.timers, onTick: () => {} });
    r.start();
    r.start();
    expect(clock.pendingCount()).toBe(1);
  });

  it('survives an empty script', () => {
    const clock = fakeClock();
    const r = createRunner({ ticks: [], timers: clock.timers, onTick: () => {} });
    r.start();
    expect(clock.pendingCount()).toBe(0);
  });
});
