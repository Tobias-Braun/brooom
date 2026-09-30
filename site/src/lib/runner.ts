/**
 * A cancellable, pausable sequencer for timed ticks. The timer functions are
 * injected so tests can drive it with a fake clock, and so the component
 * never owns a raw timer id: everything scheduled here is cleared by `stop`
 * or `pause`.
 */
import type { Tick } from './terminal-script';

export interface Timers {
  setTimeout: (fn: () => void, ms: number) => unknown;
  clearTimeout: (id: unknown) => void;
}

export interface RunnerOptions {
  ticks: readonly Tick[];
  timers: Timers;
  /** Called when a tick fires, with the tick and its index. */
  onTick: (tick: Tick, index: number) => void;
  /** Called before every restart after the last tick, so the screen can be reset. */
  onLoop?: () => void;
  /** Restart after the last tick (default true). */
  loop?: boolean;
}

export type RunnerState = 'idle' | 'running' | 'paused' | 'stopped';

export interface Runner {
  /** Starts from the first tick. */
  start(): void;
  /** Freezes at the current tick; a no-op unless running. */
  pause(): void;
  /** Continues where `pause` stopped; a no-op unless paused. */
  resume(): void;
  /** Cancels everything; the runner cannot resume afterwards. */
  stop(): void;
  readonly state: RunnerState;
}

export function createRunner(opts: RunnerOptions): Runner {
  const { ticks, timers, onTick } = opts;
  const loop = opts.loop ?? true;
  let state: RunnerState = 'idle';
  let index = 0;
  let handle: unknown = null;
  let armed = false;

  const clear = () => {
    if (armed) timers.clearTimeout(handle);
    armed = false;
    handle = null;
  };

  // Each tick waits its own delay first and is applied afterwards, so a
  // pause between two ticks re-arms only the tick that was still waiting.
  const schedule = () => {
    clear();
    const tick = ticks[index];
    if (state !== 'running' || !tick) return;
    handle = timers.setTimeout(fire, tick.delay);
    armed = true;
  };

  const fire = () => {
    armed = false;
    handle = null;
    const tick = ticks[index];
    if (state !== 'running' || !tick) return;
    onTick(tick, index);
    index += 1;
    if (index >= ticks.length) {
      if (!loop) {
        state = 'stopped';
        return;
      }
      index = 0;
      opts.onLoop?.();
    }
    schedule();
  };

  return {
    start() {
      clear();
      index = 0;
      state = 'running';
      schedule();
    },
    pause() {
      if (state !== 'running') return;
      state = 'paused';
      clear();
    },
    resume() {
      if (state !== 'paused') return;
      state = 'running';
      schedule();
    },
    stop() {
      state = 'stopped';
      clear();
    },
    get state() {
      return state;
    },
  };
}
