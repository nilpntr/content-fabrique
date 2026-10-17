import { clamp } from "./time";

export const MIN_ZOOM = 1;
export const MAX_ZOOM = 16;

/** How the timeline maps time to pixels: `pxPerMs` already includes the zoom. */
export interface TimelineScale {
  durationMs: number;
  pxPerMs: number;
}

export const clampZoom = (zoom: number) => clamp(zoom, MIN_ZOOM, MAX_ZOOM);

/** At zoom 1 the whole clip fits `viewportPx`; at zoom 2 it is twice as wide and scrolls. */
export function createScale(durationMs: number, viewportPx: number, zoom: number): TimelineScale {
  return { durationMs, pxPerMs: (viewportPx * clampZoom(zoom)) / Math.max(durationMs, 1) };
}

export const msToPx = (ms: number, scale: TimelineScale) => ms * scale.pxPerMs;

/** Pixels from the left edge of the timeline to a time, rounded to a millisecond and kept inside the clip. */
export function pxToMs(px: number, scale: TimelineScale): number {
  return clamp(Math.round(px / scale.pxPerMs), 0, scale.durationMs);
}

export const scaleWidthPx = (scale: TimelineScale) => scale.durationMs * scale.pxPerMs;

/** [step in ms, minor ticks per step]. */
const RULER_STEPS: readonly (readonly [number, number])[] = [
  [100, 5],
  [200, 4],
  [250, 5],
  [500, 5],
  [1000, 5],
  [2000, 4],
  [5000, 5],
  [10_000, 5],
  [15_000, 5],
  [30_000, 6],
  [60_000, 6],
  [120_000, 4],
  [300_000, 5],
];

export interface RulerTick {
  ms: number;
  /** A labelled tick; the others are the small marks between. */
  major: boolean;
}

/** The labelled step of the ruler: the smallest "nice" step whose labels are at least `minLabelGapPx` apart. */
export function rulerStepMs(scale: TimelineScale, minLabelGapPx = 72): number {
  const found = RULER_STEPS.find(([step]) => step * scale.pxPerMs >= minLabelGapPx);
  return (found ?? RULER_STEPS[RULER_STEPS.length - 1] ?? [300_000])[0];
}

export function rulerTicks(scale: TimelineScale, minLabelGapPx = 72): RulerTick[] {
  const step = rulerStepMs(scale, minLabelGapPx);
  const minors = RULER_STEPS.find(([s]) => s === step)?.[1] ?? 5;
  const minorStep = step / minors;
  // Skip the small marks when they would be a smudge.
  const withMinor = minorStep * scale.pxPerMs >= 6;
  const ticks: RulerTick[] = [];
  const unit = withMinor ? minorStep : step;
  for (let i = 0; i * unit <= scale.durationMs + 1e-6; i++) {
    const ms = Math.round(i * unit);
    ticks.push({ ms, major: ms % step === 0 });
  }
  return ticks;
}

export interface LaneItem {
  id: string;
  start_ms: number;
  end_ms: number;
}

/**
 * Stacks overlapping regions into rows: each goes into the first lane that is free for its width, where a region is
 * at least `minWidthPx` wide (a point comment is a dot). Returns lane indexes by id.
 */
export function assignLanes(items: LaneItem[], scale: TimelineScale, minWidthPx = 14): Map<string, number> {
  const lanes = new Map<string, number>();
  const laneEnds: number[] = [];
  const sorted = [...items].sort((a, b) => a.start_ms - b.start_ms || b.end_ms - a.end_ms || a.id.localeCompare(b.id));
  for (const item of sorted) {
    const left = msToPx(item.start_ms, scale);
    const right = left + Math.max(msToPx(item.end_ms - item.start_ms, scale), minWidthPx);
    let lane = laneEnds.findIndex((end) => end <= left);
    if (lane === -1) lane = laneEnds.length;
    laneEnds[lane] = right;
    lanes.set(item.id, lane);
  }
  return lanes;
}
