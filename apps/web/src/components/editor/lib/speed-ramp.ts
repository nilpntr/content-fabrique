/**
 * Speed ramps (timeline spec P3-10, docs/design/timeline-v1.md §2): the same maths as `internal/timeline/ramp.go`.
 *
 * A ramp is points `{at_ms, speed}`; `at_ms` is a source offset (ms from the clip's `source.in_ms`). The speed is linear in
 * source time between points and flat before the first and after the last. The output time of source offset `s` is the
 * integral of 1 / speed, and `rampSourceMs` is its inverse.
 */

export interface SpeedPoint {
  at_ms: number;
  speed: number;
}

interface Segment {
  s0: number;
  /** Infinity for the last piece. */
  s1: number;
  t0: number;
  v0: number;
  /** The slope of the speed per source ms; 0 for a constant piece. */
  k: number;
}

const outAt = (g: Segment, s: number): number => {
  const d = s - g.s0;
  return g.k === 0 ? g.t0 + d / g.v0 : g.t0 + Math.log1p((g.k * d) / g.v0) / g.k;
};
const srcAt = (g: Segment, t: number): number => {
  const u = t - g.t0;
  return g.k === 0 ? g.s0 + u * g.v0 : g.s0 + (g.v0 * Math.expm1(g.k * u)) / g.k;
};
const endOf = (g: Segment): number => (g.s1 === Number.POSITIVE_INFINITY ? Number.POSITIVE_INFINITY : outAt(g, g.s1));

function segments(points: readonly SpeedPoint[]): Segment[] {
  const usable: SpeedPoint[] = [];
  for (const p of points) {
    if (!(p.speed > 0) || !Number.isFinite(p.speed) || p.at_ms < 0) continue;
    const last = usable[usable.length - 1];
    if (last && p.at_ms <= last.at_ms) continue;
    usable.push(p);
  }
  const segs: Segment[] = [];
  let t = 0;
  const first = usable[0];
  if (first && first.at_ms > 0) {
    const g: Segment = { s0: 0, s1: first.at_ms, t0: 0, v0: first.speed, k: 0 };
    segs.push(g);
    t = endOf(g);
  }
  usable.forEach((p, i) => {
    const g: Segment = { s0: p.at_ms, s1: Number.POSITIVE_INFINITY, t0: t, v0: p.speed, k: 0 };
    const next = usable[i + 1];
    if (next) {
      g.s1 = next.at_ms;
      g.k = (next.speed - p.speed) / (g.s1 - g.s0);
      if (Math.abs(g.k) < 1e-9) g.k = 0;
      t = endOf(g);
    }
    segs.push(g);
  });
  return segs;
}

/** The output time (ms from the clip's first frame) of source offset `s` (ms from its in point). */
export function rampOutMs(points: readonly SpeedPoint[], s: number): number {
  const segs = segments(points);
  if (segs.length === 0) return s;
  if (s <= 0) return 0;
  const g = segs.find((candidate) => s <= candidate.s1) ?? segs[segs.length - 1];
  return g ? outAt(g, s) : s;
}

/** The source offset shown at output time `t`: the inverse of `rampOutMs`. */
export function rampSourceMs(points: readonly SpeedPoint[], t: number): number {
  const segs = segments(points);
  if (segs.length === 0) return t;
  if (t <= 0) return 0;
  const g = segs.find((candidate) => t <= endOf(candidate)) ?? segs[segs.length - 1];
  return g ? srcAt(g, t) : t;
}
