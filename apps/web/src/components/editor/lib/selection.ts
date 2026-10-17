import type { MsRange, Pin } from "../types";
import { pxToMs, type TimelineScale } from "./scale";
import { clamp } from "./time";

/** The shortest range worth selecting. A shorter drag is a click. */
export const MIN_RANGE_MS = 100;
/** Pointer travel under which a press-and-release is a click, not a drag. */
export const DRAG_THRESHOLD_PX = 4;
/** How close (in pixels) an edge has to be to a cut to snap to it. */
export const SNAP_PX = 8;

/** The nearest snap point within `thresholdMs`, or `ms` itself. */
export function snapMs(ms: number, points: readonly number[], thresholdMs: number): number {
  let best = ms;
  let bestDistance = thresholdMs;
  for (const point of points) {
    const distance = Math.abs(point - ms);
    if (distance <= bestDistance) {
      best = point;
      bestDistance = distance;
    }
  }
  return best;
}

export const snapThresholdMs = (scale: TimelineScale, snapPx = SNAP_PX) => snapPx / scale.pxPerMs;

/** Two times in any order become a range inside the clip. */
export function normalizeRange(a: number, b: number, durationMs: number): MsRange {
  return { start_ms: clamp(Math.min(a, b), 0, durationMs), end_ms: clamp(Math.max(a, b), 0, durationMs) };
}

export type Gesture = { kind: "point"; ms: number } | { kind: "range"; start_ms: number; end_ms: number };

/**
 * What a press at `startPx` and a release at `endPx` (pixels from the left edge of the timeline) means: a point
 * comment for a click, a range for a drag. Both ends snap to cuts that are close, unless `snap` is false.
 */
export function resolveGesture(options: {
  startPx: number;
  endPx: number;
  scale: TimelineScale;
  snapPoints?: readonly number[];
  snap?: boolean;
}): Gesture {
  const { startPx, endPx, scale, snapPoints = [], snap = true } = options;
  const threshold = snap ? snapThresholdMs(scale) : 0;
  const at = (px: number) => (snap ? snapMs(pxToMs(px, scale), snapPoints, threshold) : pxToMs(px, scale));

  const start = at(startPx);
  if (Math.abs(endPx - startPx) < DRAG_THRESHOLD_PX) return { kind: "point", ms: start };

  const range = normalizeRange(start, at(endPx), scale.durationMs);
  if (range.end_ms - range.start_ms < MIN_RANGE_MS) return { kind: "point", ms: range.start_ms };
  return { kind: "range", ...range };
}

export type Handle = "start" | "end";

/** Moves one end of a range to `ms`. The ends can't cross: the range keeps `MIN_RANGE_MS`. */
export function moveHandle(
  range: MsRange,
  handle: Handle,
  ms: number,
  options: { durationMs: number; snapPoints?: readonly number[]; thresholdMs?: number },
): MsRange {
  const { durationMs, snapPoints = [], thresholdMs = 0 } = options;
  const target = snapMs(clamp(ms, 0, durationMs), snapPoints, thresholdMs);
  if (handle === "start") {
    return { start_ms: clamp(target, 0, Math.max(range.end_ms - MIN_RANGE_MS, 0)), end_ms: range.end_ms };
  }
  return { start_ms: range.start_ms, end_ms: clamp(target, Math.min(range.start_ms + MIN_RANGE_MS, durationMs), durationMs) };
}

/**
 * The I key: the range now starts at the playhead. Without a range (or when the playhead is past its end) the range
 * runs to the end of the clip.
 */
export function markIn(range: MsRange | null, playheadMs: number, durationMs: number): MsRange {
  const start = clamp(playheadMs, 0, Math.max(durationMs - MIN_RANGE_MS, 0));
  const end = range && range.end_ms - start >= MIN_RANGE_MS ? range.end_ms : durationMs;
  return { start_ms: start, end_ms: end };
}

/**
 * The O key: the range now ends at the playhead. Without a range (or when the playhead is before its start) the range
 * starts at the beginning of the clip.
 */
export function markOut(range: MsRange | null, playheadMs: number, durationMs: number): MsRange {
  const end = clamp(playheadMs, Math.min(MIN_RANGE_MS, durationMs), durationMs);
  const start = range && end - range.start_ms >= MIN_RANGE_MS ? range.start_ms : 0;
  return { start_ms: start, end_ms: end };
}

/** A click at (`clientX`, `clientY`) on a frame whose box is `rect`, as a normalised pin. */
export function pinFromPoint(clientX: number, clientY: number, rect: { left: number; top: number; width: number; height: number }): Pin {
  const unit = (value: number) => Math.round(clamp(value, 0, 1) * 1000) / 1000;
  return {
    x: unit(rect.width > 0 ? (clientX - rect.left) / rect.width : 0.5),
    y: unit(rect.height > 0 ? (clientY - rect.top) / rect.height : 0.5),
  };
}
