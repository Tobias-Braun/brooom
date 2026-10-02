/**
 * Timing, content and motion of the hero scene: robots (AI agents) drop junk
 * on a floor, the brooom sweeper clears it, and a caption replays the matching
 * `brooom sweep` run. Everything here is pure, so canvas and caption read one
 * clock and tests can pin the caption to the real CLI output.
 *
 * The caption follows the binary: the question is confirm() in
 * internal/action/prompt.go, the counter is the "reclaimed" line of the
 * progress display (internal/cli/progressui), sizes use output.FormatSize.
 * The plan the real sweep prints before its question is left out. Item count
 * and size are computed from the junk in the scene, so both always agree.
 */

/**
 * The loop in seconds. To retime the scene, move a boundary here: nothing
 * else holds a point in time. The robots share `robots` (each walks for the
 * phase minus the stagger of the robots after it), `brooom sweep` is typed
 * across `type`, the question waits for its answer during `ask` and gets its
 * `y` when `ask` ends, the sweeper slides in during `enter`, crosses during
 * `sweep` and leaves during `exit`, and `done` holds the clean floor with the
 * final caption, which fades out over the last FADE_S. The constants below
 * tune the beats inside a phase.
 */
export const TIMELINE = {
  idle: [0, 0.5],
  robots: [0.5, 4.5],
  type: [4.5, 5.2],
  ask: [5.2, 6.2],
  enter: [6.2, 6.5],
  sweep: [6.5, 7.4],
  exit: [7.4, 7.8],
  done: [7.8, 9],
} as const;

export const LOOP_S = TIMELINE.done[1];
export const ROBOT_STAGGER_S = 0.8;
const DROP_S = 0.35;
export const FLICK_S = 0.5;
/** The caption counts an item's bytes over this long once the sweeper reaches it. */
const COUNT_S = 0.1;
const PUFF_S = 0.6;
const FADE_S = 0.3;
const STEPS_PER_S = 6;
const SWING_PER_S = 16;

export const COMMAND = 'brooom sweep';

/** Floor positions run from 0 (left edge) to 1 (right edge). */
const ROBOT_U = [-0.08, 1.08] as const;
/** Sweeper contact point when enter, sweep and exit start, and when exit ends. */
const SWEEPER_U = [-0.25, 0.04, 0.96, 1.3] as const;
const DROP_BEHIND_U = 0.03;
const DROP_HEIGHT = 0.5;

export type JunkKind = 'worktree' | 'branch' | 'log' | 'deps' | 'cache' | 'core' | 'temp';

/** Label under the item, size range in bytes and drawing scale: bigger on disk is bigger on screen. */
export const JUNK: Record<JunkKind, { label: string; min: number; max: number; scale: number }> = {
  deps: { label: 'node_modules', min: 3e8, max: 9e8, scale: 1 },
  worktree: { label: 'worktree', min: 2e8, max: 1.2e9, scale: 0.95 },
  cache: { label: 'cache', min: 1e8, max: 5e8, scale: 0.85 },
  core: { label: 'core dump', min: 5e7, max: 3e8, scale: 0.75 },
  log: { label: 'logs', min: 5e6, max: 8e7, scale: 0.7 },
  branch: { label: 'branch', min: 0, max: 0, scale: 0.65 },
  temp: { label: 'tmp', min: 2e4, max: 5e6, scale: 0.45 },
};

export interface Robot {
  start: number;
  walk: number;
  lane: number;
  tint: number;
  size: number;
}

export interface Item {
  kind: JunkKind;
  u: number;
  lane: number;
  bytes: number;
  seed: number;
  drop: number;
  flick: number;
}

export interface Plan {
  robots: Robot[];
  items: Item[];
  total: number;
}

export interface Scene {
  robots: { robot: Robot; u: number; step: number }[];
  items: { item: Item; u: number; z: number; spin: number; alpha: number }[];
  sweeper: { u: number; swing: number } | null;
  puffs: { u: number; lane: number; p: number }[];
}

export interface Caption {
  lines: string[];
  /** Index of the line that shows the cursor, -1 for none. */
  cursor: number;
  opacity: number;
}

const clamp01 = (x: number) => Math.min(1, Math.max(0, x));
const lerp = (a: number, b: number, p: number) => a + (b - a) * p;
const progress = (t: number, [a, b]: readonly [number, number]) => (t - a) / (b - a);

function shuffle<T>(list: T[], rand: () => number): T[] {
  const out = [...list];
  for (let i = out.length - 1; i > 0; i--) {
    const j = Math.floor(rand() * (i + 1));
    [out[i], out[j]] = [out[j]!, out[i]!];
  }
  return out;
}

/** A seeded generator (mulberry32), so the static view and tests get fixed plans. */
export function seeded(seed: number): () => number {
  return () => {
    seed = (seed + 0x6d2b79f5) | 0;
    let x = Math.imul(seed ^ (seed >>> 15), seed | 1);
    x ^= x + Math.imul(x ^ (x >>> 7), x | 61);
    return ((x ^ (x >>> 14)) >>> 0) / 2 ** 32;
  };
}

/** Rolls one loop: which robots walk where, and what each of them drops. */
export function makePlan(rand: () => number, compact: boolean): Plan {
  const [start, end] = TIMELINE.robots;
  const count = compact ? 2 : 3;
  const walk = end - start - (count - 1) * ROBOT_STAGGER_S;
  const lanes = shuffle([-1, 0, 1], rand);
  const tints = shuffle([0, 1, 2], rand);
  const robots = lanes.slice(0, count).map((lane, i) => ({
    start: start + i * ROBOT_STAGGER_S,
    walk,
    lane,
    tint: tints[i]!,
    size: 0.9 + rand() * 0.2,
  }));
  const drops = robots.map(() => (compact ? 1 : 2) + Math.floor(rand() * 2));
  const n = drops.reduce((a, b) => a + b, 0);
  // Every item gets its own slot across the floor, so nothing overlaps. The
  // slots end short of the right edge, so the counter is full when exit starts.
  const slots = shuffle([...Array(n).keys()], rand);
  const others = shuffle((Object.keys(JUNK) as JunkKind[]).filter((k) => k !== 'worktree'), rand);
  const items = robots.flatMap((robot, r) =>
    Array.from({ length: drops[r]! }, (_, k) => {
      const u = 0.08 + ((slots.pop()! + 0.2 + rand() * 0.6) / n) * 0.77;
      // Every agent works in its own worktree, so each robot leaves one behind.
      const kind: JunkKind = k === 0 ? 'worktree' : others[slots.length % others.length]!;
      const { min, max } = JUNK[kind];
      return {
        kind,
        u,
        lane: robot.lane,
        bytes: Math.round(lerp(min, max, rand())),
        seed: rand(),
        drop: robot.start + (robot.walk * (u + DROP_BEHIND_U - ROBOT_U[0])) / (ROBOT_U[1] - ROBOT_U[0]),
        flick:
          TIMELINE.sweep[0] +
          ((u - SWEEPER_U[1]) / (SWEEPER_U[2] - SWEEPER_U[1])) * (TIMELINE.sweep[1] - TIMELINE.sweep[0]),
      };
    }),
  );
  return { robots, items, total: items.reduce((sum, it) => sum + it.bytes, 0) };
}

function sweeperU(t: number): number | null {
  const { enter, sweep, exit } = TIMELINE;
  if (t < enter[0] || t >= exit[1]) return null;
  if (t < sweep[0]) return lerp(SWEEPER_U[0], SWEEPER_U[1], 1 - (1 - progress(t, enter)) ** 2);
  if (t < exit[0]) return lerp(SWEEPER_U[1], SWEEPER_U[2], progress(t, sweep));
  return lerp(SWEEPER_U[2], SWEEPER_U[3], progress(t, exit) ** 2);
}

/** Bytes freed by time t: each item counts up as the sweeper reaches it. */
export function reclaimed(t: number, plan: Plan): number {
  return plan.items.reduce((sum, it) => sum + it.bytes * clamp01((t - it.flick) / COUNT_S), 0);
}

/** Where everything is at time t of the loop. */
export function sceneAt(t: number, plan: Plan): Scene {
  const robots = plan.robots
    .filter((r) => t >= r.start && t < r.start + r.walk)
    .map((robot) => ({
      robot,
      u: lerp(ROBOT_U[0], ROBOT_U[1], (t - robot.start) / robot.walk),
      step: Math.floor(t * STEPS_PER_S) % 2,
    }));
  const items = plan.items
    .filter((it) => t >= it.drop && t < it.flick + FLICK_S)
    .map((item) => {
      if (t < item.flick) {
        // Falls from the robot's back, then one small bounce.
        const p = clamp01((t - item.drop) / DROP_S);
        const z = p < 0.7 ? DROP_HEIGHT * (1 - (p / 0.7) ** 2) : 0.2 * DROP_HEIGHT * Math.sin((Math.PI * (p - 0.7)) / 0.3);
        return { item, u: item.u, z, spin: 0, alpha: 1 };
      }
      const p = (t - item.flick) / FLICK_S;
      return { item, u: item.u + 0.05 * p, z: 2.4 * p * (1 - 0.5 * p), spin: 3 * p, alpha: 1 - p };
    });
  const puffs = plan.items
    .filter((it) => t >= it.flick && t < it.flick + PUFF_S)
    .map((it) => ({ u: it.u, lane: it.lane, p: (t - it.flick) / PUFF_S }));
  const u = sweeperU(t);
  return { robots, items, puffs, sweeper: u === null ? null : { u, swing: Math.sin(t * SWING_PER_S) } };
}

/**
 * The fixed plan of the static view (server rendering, screen readers,
 * reduced motion). Seed 12 rolls a small, typical set: two worktrees, logs
 * and node_modules. Compact, so its labels fit half the floor.
 */
export const staticPlan = () => makePlan(seeded(12), true);

/**
 * The reduced-motion picture: junk on the left half, sweeper in the middle,
 * the right half clean. The junk is spread evenly over alternating lanes, so
 * the size labels of neighbours do not collide in the narrower space.
 */
export function staticScene(plan: Plan): Scene {
  const items = [...plan.items].sort((a, b) => a.u - b.u);
  return {
    robots: [],
    items: items.map((item, i) => ({
      item: { ...item, lane: i % 2 ? 1 : -1 },
      u: 0.05 + ((i + 0.5) / items.length) * 0.36,
      z: 0,
      spin: 0,
      alpha: 1,
    })),
    sweeper: { u: 0.5, swing: 0 },
    puffs: [],
  };
}

const UNITS = ['B', 'kB', 'MB', 'GB', 'TB', 'PB'];

/** output.FormatSize: decimal units, one decimal, rounded half up in integers. */
export function formatSize(bytes: number): string {
  const n = Math.floor(bytes);
  if (n <= 0) return '0 B';
  if (n < 1000) return `${n} B`;
  let unit = 1;
  let div = 1000;
  while (unit < UNITS.length - 1 && n >= div * 1000) {
    unit++;
    div *= 1000;
  }
  let tenths = Math.floor((n + div / 20) / (div / 10));
  if (tenths >= 10000 && unit < UNITS.length - 1) {
    unit++;
    div *= 1000;
    tenths = Math.floor((n + div / 20) / (div / 10));
  }
  return `${Math.floor(tenths / 10)}.${tenths % 10} ${UNITS[unit]}`;
}

export function question(plan: Plan): string {
  const n = plan.items.length;
  return `Proceed with ${n} ${n === 1 ? 'item' : 'items'} (${formatSize(plan.total)})? [y/N] `;
}

/** The finished run, for screen readers, reduced motion and no JavaScript. */
export function transcript(plan: Plan): string[] {
  return [`$ ${COMMAND}`, `${question(plan)}y`, `reclaimed ${formatSize(plan.total)}`];
}

/** The caption at time t of the loop. */
export function captionAt(t: number, plan: Plan): Caption {
  const { type, ask, sweep } = TIMELINE;
  const typed = Math.min(COMMAND.length, Math.floor((COMMAND.length + 1) * clamp01(progress(t, type))));
  const lines = [`$ ${COMMAND.slice(0, typed)}`];
  if (t >= ask[0]) lines.push(question(plan) + (t >= ask[1] ? 'y' : ''));
  if (t >= sweep[0]) lines.push(`reclaimed ${formatSize(reclaimed(t, plan))}`);
  return { lines, cursor: t < sweep[0] ? lines.length - 1 : -1, opacity: clamp01((LOOP_S - t) / FADE_S) };
}
