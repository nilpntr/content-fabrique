import { expect, test } from "vitest";
import { createScale } from "./scale";
import { markIn, markOut, moveHandle, normalizeRange, pinFromPoint, resolveGesture, snapMs } from "./selection";

// 10 s over 1000 px: 100 px per second, 8 px is 80 ms.
const scale = createScale(10_000, 1000, 1);

test("snapMs picks the nearest point within the threshold and leaves other times alone", () => {
  expect(snapMs(4040, [4000, 8000], 80)).toBe(4000);
  expect(snapMs(4100, [4000, 8000], 80)).toBe(4100);
  expect(snapMs(3990, [3960, 4000], 80)).toBe(4000);
  expect(snapMs(123, [], 80)).toBe(123);
});

test("normalizeRange sorts and clamps", () => {
  expect(normalizeRange(5000, 2000, 10_000)).toEqual({ start_ms: 2000, end_ms: 5000 });
  expect(normalizeRange(-300, 12_000, 10_000)).toEqual({ start_ms: 0, end_ms: 10_000 });
});

test("a drag from 200 px to 500 px selects 2000 to 5000 ms", () => {
  expect(resolveGesture({ startPx: 200, endPx: 500, scale })).toEqual({ kind: "range", start_ms: 2000, end_ms: 5000 });
});

test("dragging right to left selects the same range", () => {
  expect(resolveGesture({ startPx: 500, endPx: 200, scale })).toEqual({ kind: "range", start_ms: 2000, end_ms: 5000 });
});

test("a press and release without travel is a point comment", () => {
  expect(resolveGesture({ startPx: 300, endPx: 301, scale })).toEqual({ kind: "point", ms: 3000 });
  expect(resolveGesture({ startPx: 300, endPx: 300, scale })).toEqual({ kind: "point", ms: 3000 });
});

test("both ends of a drag snap to cuts that are within 8 px", () => {
  const cuts = [4000, 8000];
  // 397 px is 3970 ms (3 px from the cut), 803 px is 8030 ms.
  expect(resolveGesture({ startPx: 397, endPx: 803, scale, snapPoints: cuts })).toEqual({ kind: "range", start_ms: 4000, end_ms: 8000 });
  // 12 px away is too far.
  expect(resolveGesture({ startPx: 388, endPx: 812, scale, snapPoints: cuts })).toEqual({ kind: "range", start_ms: 3880, end_ms: 8120 });
});

test("snapping can be switched off", () => {
  expect(resolveGesture({ startPx: 397, endPx: 803, scale, snapPoints: [4000, 8000], snap: false })).toEqual({
    kind: "range",
    start_ms: 3970,
    end_ms: 8030,
  });
});

test("the snap distance is in pixels, so zooming in snaps closer", () => {
  const zoomed = createScale(10_000, 1000, 8);
  // 8 px at 0.8 px/ms is 10 ms: 3970 ms is 30 ms from the cut, too far to snap now.
  expect(resolveGesture({ startPx: 3176, endPx: 4000, scale: zoomed, snapPoints: [4000] })).toMatchObject({ start_ms: 3970 });
});

test("a drag outside the timeline stays inside the clip", () => {
  expect(resolveGesture({ startPx: 900, endPx: 1400, scale })).toEqual({ kind: "range", start_ms: 9000, end_ms: 10_000 });
});

test("moving a handle keeps the range at least 100 ms long", () => {
  const range = { start_ms: 2000, end_ms: 5000 };
  const options = { durationMs: 10_000 };
  expect(moveHandle(range, "end", 3000, options)).toEqual({ start_ms: 2000, end_ms: 3000 });
  expect(moveHandle(range, "end", 1000, options)).toEqual({ start_ms: 2000, end_ms: 2100 });
  expect(moveHandle(range, "start", 9000, options)).toEqual({ start_ms: 4900, end_ms: 5000 });
  expect(moveHandle(range, "start", -50, options)).toEqual({ start_ms: 0, end_ms: 5000 });
  expect(moveHandle(range, "end", 20_000, options)).toEqual({ start_ms: 2000, end_ms: 10_000 });
});

test("a handle snaps to a cut within the threshold", () => {
  const range = { start_ms: 2000, end_ms: 5000 };
  const options = { durationMs: 10_000, snapPoints: [4000], thresholdMs: 80 };
  expect(moveHandle(range, "end", 4050, options)).toEqual({ start_ms: 2000, end_ms: 4000 });
  expect(moveHandle(range, "end", 4200, options)).toEqual({ start_ms: 2000, end_ms: 4200 });
});

test("I and O set the ends at the playhead", () => {
  const range = { start_ms: 2000, end_ms: 5000 };
  expect(markIn(range, 3000, 10_000)).toEqual({ start_ms: 3000, end_ms: 5000 });
  expect(markOut(range, 4000, 10_000)).toEqual({ start_ms: 2000, end_ms: 4000 });
});

test("I with no range selects to the end of the clip and O selects from the start", () => {
  expect(markIn(null, 3000, 10_000)).toEqual({ start_ms: 3000, end_ms: 10_000 });
  expect(markOut(null, 4000, 10_000)).toEqual({ start_ms: 0, end_ms: 4000 });
});

test("I past the end of the range, or O before its start, resets the other end", () => {
  const range = { start_ms: 2000, end_ms: 5000 };
  expect(markIn(range, 6000, 10_000)).toEqual({ start_ms: 6000, end_ms: 10_000 });
  expect(markOut(range, 1000, 10_000)).toEqual({ start_ms: 0, end_ms: 1000 });
});

test("I at the very end still leaves a range", () => {
  expect(markIn(null, 10_000, 10_000)).toEqual({ start_ms: 9900, end_ms: 10_000 });
});

test("a click on the frame becomes a normalised pin", () => {
  const rect = { left: 100, top: 50, width: 200, height: 400 };
  expect(pinFromPoint(200, 250, rect)).toEqual({ x: 0.5, y: 0.5 });
  expect(pinFromPoint(100, 50, rect)).toEqual({ x: 0, y: 0 });
  expect(pinFromPoint(999, -10, rect)).toEqual({ x: 1, y: 0 });
  expect(pinFromPoint(10, 10, { left: 0, top: 0, width: 0, height: 0 })).toEqual({ x: 0.5, y: 0.5 });
});
