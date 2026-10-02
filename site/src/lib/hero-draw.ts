/**
 * Paints a hero Scene on a 2D canvas in a flat-shaded isometric look: every
 * solid shows its top, left and right face in three tones of one hue, all
 * derived from the page's color tokens. Drawing functions work in world units
 * around the current floor point (ox, oy): x runs down-right, y down-left,
 * z up, so walking along x - y is screen-horizontal.
 */
import { JUNK, formatSize, type Item, type Scene } from './hero-timeline';

export interface Palette {
  bg: string;
  surface: string;
  elevated: string;
  border: string;
  text: string;
  muted: string;
  violet: string;
  blue: string;
  neon: string;
  mono: string;
}

export function readPalette(el: Element): Palette {
  const css = getComputedStyle(el);
  const v = (name: string) => css.getPropertyValue(`--${name}`).trim();
  return {
    bg: v('bg'),
    surface: v('surface'),
    elevated: v('bg-elevated'),
    border: v('border'),
    text: v('text'),
    muted: v('text-muted'),
    violet: v('violet'),
    blue: v('blue'),
    neon: v('neon'),
    mono: v('font-mono'),
  };
}

const C = Math.cos(Math.PI / 6);
const FLOOR_Y = 0.62;
/** The floor has rows -ROWS..ROWS of tiles; robots and junk use lanes -1..1. */
const ROWS = 1;
const LANE = 0.5;
/** The brooom mark (BroomMark.astro) in its 64-unit viewBox. */
const HANDLE = new Path2D('M56 6 35 27');
const HEAD = new Path2D('M32.2 26.2 37.8 31.8 21.2 61.2 2.8 42.8Z');
const GROOVES = new Path2D('M36.4 30.4 16.6 56.6M33.6 27.6 7.4 47.4');
const DUST = new Path2D('M46 56h8M52 49h6');

type Pt = [number, number];
let g: CanvasRenderingContext2D;
let P: Palette;
let T = 1;
let S = 1;
let ox = 0;
let oy = 0;

const at = (x: number, y: number, z: number): Pt => [ox + (x - y) * C * T * S, oy + ((x + y) / 2 - z) * T * S];
const lerp = (a: number, b: number, p: number) => a + (b - a) * p;

function hexMix(a: string, b: string, f: number): string {
  const ca = parseInt(a.slice(1), 16);
  const cb = parseInt(b.slice(1), 16);
  let out = 0;
  for (const s of [16, 8, 0]) out |= Math.round(lerp((ca >> s) & 255, (cb >> s) & 255, f)) << s;
  return `#${out.toString(16).padStart(6, '0')}`;
}

const toneCache = new Map<string, string[]>();
/** Top, left and right face color of one hue: lit, base, shaded. */
function tones(hex: string): string[] {
  let t = toneCache.get(hex);
  if (!t) toneCache.set(hex, (t = [hexMix(hex, '#ffffff', 0.25), hex, hexMix(hex, '#000000', 0.38)]));
  return t;
}

function fill(pts: Pt[], color: string) {
  g.beginPath();
  for (const [x, y] of pts) g.lineTo(x, y);
  g.closePath();
  g.fillStyle = color;
  g.fill();
}

function line(pts: Pt[], color: string, width: number) {
  g.beginPath();
  for (const [x, y] of pts) g.lineTo(x, y);
  g.strokeStyle = color;
  g.lineWidth = width * T * S;
  g.stroke();
}

function dot([x, y]: Pt, r: number, color: string) {
  g.beginPath();
  g.arc(x, y, r * T * S, 0, 2 * Math.PI);
  g.fillStyle = color;
  g.fill();
}

/** A box centered on (x, y) with its base at height z. */
function box(x: number, y: number, z: number, w: number, d: number, h: number, hex: string) {
  const [top, left, right] = tones(hex);
  const [a, b, c, e, z1] = [x - w / 2, x + w / 2, y - d / 2, y + d / 2, z + h];
  fill([at(a, e, z), at(b, e, z), at(b, e, z1), at(a, e, z1)], left!);
  fill([at(b, c, z), at(b, e, z), at(b, e, z1), at(b, c, z1)], right!);
  fill([at(a, c, z1), at(b, c, z1), at(b, e, z1), at(a, e, z1)], top!);
}

function shadow(r: number) {
  g.beginPath();
  g.ellipse(ox, oy, r * C * T * S * 1.4, r * T * S * 0.7, 0, 0, 2 * Math.PI);
  g.fillStyle = 'rgb(0 0 0 / 0.3)';
  g.fill();
}

function floor(w: number, h: number) {
  const step = 2 * C * T;
  const [, left, right] = tones(P.elevated);
  for (let k = -ROWS; k <= ROWS; k++) {
    const y = h * FLOOR_Y + (k * T) / 2;
    for (let i = 0, x = (k & 1) * C * T - step; x < w + step; i++, x += step) {
      const tile: Pt[] = [[x, y - T / 2], [x + C * T, y], [x, y + T / 2], [x - C * T, y]];
      fill(tile, i % 2 ? P.surface : hexMix(P.surface, P.border, 0.3));
      g.strokeStyle = P.border;
      g.lineWidth = 1;
      g.stroke();
      if (k === ROWS) {
        const d = T / 4;
        fill([tile[3]!, tile[2]!, [x, y + T / 2 + d], [x - C * T, y + d]], left!);
        fill([tile[2]!, tile[1]!, [x + C * T, y + d], [x, y + T / 2 + d]], right!);
      }
    }
  }
}

function robot(step: number, tint: number) {
  const hue = [P.violet, P.blue, P.neon][tint]!;
  const bob = step ? 0.05 : 0;
  box(-0.07, -0.07, step ? 0 : 0.05, 0.1, 0.1, 0.08, P.muted);
  box(0.07, 0.07, step ? 0.05 : 0, 0.1, 0.1, 0.08, P.muted);
  box(0, 0, 0.08 + bob, 0.26, 0.26, 0.22, hue);
  box(0, 0, 0.3 + bob, 0.34, 0.34, 0.26, hexMix(hue, P.text, 0.25));
  dot(at(0.17, -0.08, 0.44 + bob), 0.035, P.bg);
  dot(at(0.17, 0.07, 0.44 + bob), 0.035, P.bg);
  line([at(0, 0, 0.56 + bob), at(0, 0, 0.8 + bob)], P.muted, 0.03);
  dot(at(0, 0, 0.82 + bob), 0.1, `${P.neon}${step ? '33' : '55'}`);
  dot(at(0, 0, 0.82 + bob), 0.05, P.neon);
}

/** Maps (s, t) onto the left face of a box whose front-left bottom corner is (x, y, z): s runs along x, t up. */
function onLeftFace(x: number, y: number, z: number) {
  const [px, py] = at(x, y, z);
  g.transform(C * T * S, (T * S) / 2, 0, -T * S, px, py);
}

const ITEMS: Record<Item['kind'], (it: Item) => void> = {
  worktree() {
    box(0, 0, 0, 0.75, 0.5, 0.45, P.violet);
    box(-0.2, 0, 0.45, 0.3, 0.5, 0.06, P.violet);
    g.save();
    onLeftFace(-0.375, 0.25, 0);
    g.beginPath();
    g.moveTo(0.22, 0.1);
    g.lineTo(0.22, 0.35);
    g.moveTo(0.22, 0.17);
    g.quadraticCurveTo(0.5, 0.17, 0.52, 0.33);
    g.strokeStyle = P.text;
    g.lineWidth = 0.04;
    g.stroke();
    for (const [s, t] of [[0.22, 0.1], [0.22, 0.35], [0.52, 0.33]]) {
      g.beginPath();
      g.arc(s!, t!, 0.04, 0, 2 * Math.PI);
      g.fillStyle = P.text;
      g.fill();
    }
    g.restore();
  },
  branch() {
    const grey = hexMix(P.muted, P.bg, 0.15);
    box(0, 0, 0, 0.09, 0.09, 0.55, grey);
    const [x, y] = at(0, 0, 0.5);
    const u = T * S;
    g.lineCap = 'round';
    for (const [cx, cy, ex, ey] of [[-0.3, -0.25, -0.38, 0.12], [0.28, -0.12, 0.34, 0.2]]) {
      g.beginPath();
      g.moveTo(x, y);
      g.quadraticCurveTo(x + cx! * u, y + cy! * u, x + ex! * u, y + ey! * u);
      g.lineWidth = 0.06 * u;
      g.strokeStyle = grey;
      g.stroke();
      dot([x + ex! * u, y + ey! * u], 0.055, tones(grey)[2]!);
    }
    // A tiny cobweb in the right fork: spokes and two threads across them.
    const spokes = [-1.2, -0.5, 0.2].map((a) => [x + Math.cos(a) * 0.2 * u, y + Math.sin(a) * 0.2 * u] as Pt);
    g.globalAlpha *= 0.5;
    for (const p of spokes) line([[x, y], p], P.text, 0.012);
    for (const f of [0.5, 1]) line(spokes.map(([px, py]) => [lerp(x, px, f), lerp(y, py, f)] as Pt), P.text, 0.012);
    g.globalAlpha /= 0.5;
  },
  log(it) {
    const paper = hexMix(P.text, P.muted, 0.3);
    for (let i = 0; i < 7; i++) {
      const j = Math.sin(it.seed * 50 + i * 2.3) * 0.03;
      box(j, -j, i * 0.075, 0.42, 0.52, 0.05, paper);
    }
    for (const y of [-0.14, 0, 0.14]) line([at(-0.12, y, 0.5), at(0.14, y, 0.5)], P.muted, 0.025);
  },
  deps() {
    const crate = hexMix(P.blue, P.bg, 0.45);
    box(0, 0, 0, 0.7, 0.7, 0.4, crate);
    const dark = tones(crate)[2]!;
    line([at(-0.35, 0.35, 0.2), at(0.35, 0.35, 0.2), at(0.35, -0.35, 0.2)], dark, 0.03);
    box(-0.16, -0.16, 0.4, 0.26, 0.26, 0.26, P.violet);
    box(0.16, -0.16, 0.4, 0.26, 0.26, 0.26, P.neon);
    box(-0.16, 0.16, 0.4, 0.26, 0.26, 0.26, P.blue);
    box(0.16, 0.16, 0.4, 0.26, 0.26, 0.26, P.violet);
    box(0, 0, 0.66, 0.24, 0.24, 0.24, P.blue);
  },
  cache() {
    const [top, side, dark] = tones(hexMix(P.neon, P.bg, 0.35));
    const rx = 0.3 * Math.SQRT2 * C * T * S;
    const ry = 0.15 * Math.SQRT2 * T * S;
    const [, yb] = at(0, 0, 0);
    const [, yt] = at(0, 0, 0.6);
    g.save();
    g.beginPath();
    g.ellipse(ox, yb, rx, ry, 0, 0, Math.PI);
    g.lineTo(ox - rx, yt);
    g.lineTo(ox + rx, yt);
    g.closePath();
    g.fillStyle = side!;
    g.fill();
    g.clip();
    g.fillStyle = dark!;
    g.fillRect(ox, yt, rx, yb - yt + ry);
    g.restore();
    for (const z of [0.15, 0.45]) {
      g.beginPath();
      g.ellipse(ox, at(0, 0, z)[1], rx, ry, 0, 0, Math.PI);
      g.strokeStyle = dark!;
      g.lineWidth = 0.035 * T * S;
      g.stroke();
    }
    g.beginPath();
    g.ellipse(ox, yt, rx, ry, 0, 0, 2 * Math.PI);
    g.fillStyle = top!;
    g.fill();
  },
  core() {
    box(0, 0, 0, 0.55, 0.55, 0.5, hexMix(P.violet, P.bg, 0.5));
    line([at(-0.08, -0.12, 0.5), at(0.02, 0.05, 0.5), at(-0.1, 0.275, 0.5), at(0.0, 0.275, 0.33), at(-0.12, 0.275, 0.2), at(-0.03, 0.275, 0.05)], P.bg, 0.04);
  },
  temp(it) {
    const paper = tones(hexMix(P.text, P.muted, 0.2));
    const [cx, cy] = at(0, 0, 0.2);
    const pts = Array.from({ length: 8 }, (_, i) => {
      const a = (i / 8) * 2 * Math.PI;
      const r = (0.17 + 0.05 * Math.sin(it.seed * 90 + i * 1.7)) * T * S;
      return [cx + Math.cos(a) * r, cy + Math.sin(a) * r * 0.9] as Pt;
    });
    pts.forEach((p, i) => {
      const q = pts[(i + 1) % 8]!;
      const mid = Math.atan2((p[1] + q[1]) / 2 - cy, (p[0] + q[0]) / 2 - cx);
      fill([[cx, cy], p, q], paper[mid < -0.6 ? 0 : mid < 1.6 ? 2 : 1]!);
    });
  },
};

function sweeper(swing: number, front: CanvasGradient) {
  const k = (1.8 * T) / 58;
  const depth = (0.22 * T) / k;
  const [, side, back] = tones(tones(hexMix(P.violet, P.blue, 0.5))[2]!);
  g.save();
  g.translate(ox, oy);
  g.scale(k, k);
  g.translate(-21.2, -61.2);
  g.lineCap = 'round';
  g.lineJoin = 'round';
  // Extruded by stacking the mark from back to front along world x, across
  // the handle, so the thickness shows below and right of every edge.
  for (let i = 6; i >= 0; i--) {
    g.save();
    g.translate((C * depth * i) / 6, (depth * i) / 12);
    g.fillStyle = g.strokeStyle = i ? (i === 6 ? back! : side!) : front;
    g.lineWidth = 5;
    g.stroke(HANDLE);
    g.translate(35, 27);
    g.rotate(swing * 0.12);
    g.translate(-35, -27);
    g.lineWidth = 3;
    g.fill(HEAD);
    g.stroke(HEAD);
    if (!i) {
      g.strokeStyle = P.bg;
      g.lineWidth = 2;
      g.stroke(GROOVES);
      g.strokeStyle = side!;
      g.lineWidth = 1.6;
      for (let s = 1; s < 6; s++) {
        const [bx, by] = [lerp(2.8, 21.2, s / 6), lerp(42.8, 61.2, s / 6)];
        g.beginPath();
        g.moveTo(bx, by);
        g.lineTo(bx - 3 + swing * 2, by + 3 + swing);
        g.stroke();
      }
    }
    g.restore();
  }
  g.globalAlpha *= 0.6 + 0.4 * Math.abs(swing);
  g.strokeStyle = P.neon;
  g.lineWidth = 2.5;
  g.stroke(DUST);
  g.restore();
}

function puff(p: number) {
  g.globalAlpha = (1 - p) * 0.5;
  for (let i = 0; i < 7; i++) {
    const a = i * 0.9 - 2.8;
    const r = (0.03 + 0.07 * p) * (0.6 + 0.2 * (i % 3));
    dot([ox + Math.cos(a) * p * 0.6 * T, oy - p * 0.6 * T + Math.sin(a) * p * 0.3 * T], r, P.muted);
  }
  g.globalAlpha = 1;
}

/** Clears the canvas and paints one frame; w and h are CSS pixels. */
export function paint(ctx: CanvasRenderingContext2D, w: number, h: number, dpr: number, scene: Scene, palette: Palette) {
  g = ctx;
  P = palette;
  T = Math.min(h / 4, w / 9);
  g.setTransform(dpr, 0, 0, dpr, 0, 0);
  g.clearRect(0, 0, w, h);
  S = 1;
  floor(w, h);
  const place = (u: number, lane: number) => {
    ox = u * w;
    oy = h * FLOOR_Y + lane * LANE * T;
  };
  const solids: [number, number, () => void][] = [
    ...scene.robots.map(({ robot: r, u, step }): [number, number, () => void] => [r.lane, u, () => {
      place(u, r.lane);
      S = r.size;
      shadow(0.2);
      robot(step, r.tint);
    }]),
    ...scene.items.map(({ item, u, z, spin, alpha }): [number, number, () => void] => [item.lane, u, () => {
      place(u, item.lane);
      const { min, max, scale } = JUNK[item.kind];
      S = scale * (0.9 + (0.2 * (item.bytes - min)) / (max - min || 1));
      g.globalAlpha = alpha;
      shadow(0.3 * (1 - Math.min(1, z / 2)));
      oy -= z * T;
      g.save();
      const [px, py] = at(0, 0, 0.25);
      g.translate(px, py);
      g.rotate(spin);
      g.translate(-px, -py);
      ITEMS[item.kind](item);
      g.restore();
      g.globalAlpha = 1;
    }]),
  ];
  solids.sort((a, b) => a[0] - b[0] || a[1] - b[1]).forEach(([, , draw]) => draw());
  // Labels go above every item, so items in front never hide them. Each
  // kind has its own color, the item's main hue. Branches have no size, so
  // like the CLI they only get their name.
  const font = Math.max(10, Math.round(T * 0.17));
  const colors: Record<Item['kind'], string> = {
    worktree: P.violet,
    deps: P.blue,
    cache: P.neon,
    core: hexMix(P.violet, P.text, 0.5),
    log: P.text,
    temp: P.muted,
    branch: hexMix(P.muted, P.bg, 0.25),
  };
  g.font = `${font}px ${P.mono}`;
  g.textAlign = 'center';
  g.textBaseline = 'top';
  g.strokeStyle = P.bg;
  g.lineWidth = 3;
  g.lineJoin = 'round';
  for (const { item, u, alpha } of scene.items) {
    place(u, item.lane);
    g.globalAlpha = alpha;
    g.fillStyle = colors[item.kind];
    const lines = item.bytes ? [JUNK[item.kind].label, formatSize(item.bytes)] : [JUNK[item.kind].label];
    lines.forEach((text, i) => {
      const y = oy + 0.42 * T + i * font * 1.15;
      // A halo in the page background keeps the label readable where it crosses an item.
      g.strokeText(text, ox, y);
      g.fillText(text, ox, y);
    });
  }
  g.globalAlpha = 1;
  S = 1;
  // The sweeper plows through everything, so it goes on top of items and labels.
  if (scene.sweeper) {
    place(scene.sweeper.u, 0);
    const front = g.createLinearGradient(8, 58, 58, 6);
    front.addColorStop(0, P.violet);
    front.addColorStop(1, P.blue);
    sweeper(scene.sweeper.swing, front);
  }
  for (const p of scene.puffs) {
    place(p.u, p.lane);
    puff(p.p);
  }
}
