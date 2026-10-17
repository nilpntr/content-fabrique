import { expect, test } from "vitest";
import { assignLanes, clampZoom, createScale, msToPx, pxToMs, rulerStepMs, rulerTicks, scaleWidthPx } from "./scale";

test("at zoom 1 the clip fills the viewport", () => {
  const scale = createScale(10_000, 1000, 1);
  expect(scaleWidthPx(scale)).toBe(1000);
  expect(msToPx(2000, scale)).toBe(200);
  expect(pxToMs(500, scale)).toBe(5000);
});

test("zoom multiplies the width and the pixels per millisecond", () => {
  const scale = createScale(10_000, 1000, 4);
  expect(scaleWidthPx(scale)).toBe(4000);
  expect(msToPx(2000, scale)).toBe(800);
  expect(pxToMs(800, scale)).toBe(2000);
});

test("ms and px round-trip at every zoom", () => {
  for (const zoom of [1, 2, 3, 8, 16]) {
    const scale = createScale(12_345, 913, zoom);
    for (const ms of [0, 1, 999, 4321, 12_345]) {
      expect(pxToMs(msToPx(ms, scale), scale)).toBe(ms);
    }
  }
});

test("pixels outside the timeline clamp to the clip", () => {
  const scale = createScale(10_000, 1000, 1);
  expect(pxToMs(-40, scale)).toBe(0);
  expect(pxToMs(5000, scale)).toBe(10_000);
});

test("zoom is clamped to 1 to 16", () => {
  expect(clampZoom(0.2)).toBe(1);
  expect(clampZoom(64)).toBe(16);
  expect(createScale(10_000, 1000, 100).pxPerMs).toBe(1.6);
});

test("the ruler picks a labelled step that is wide enough, and refines as you zoom in", () => {
  expect(rulerStepMs(createScale(10_000, 1000, 1))).toBe(1000);
  expect(rulerStepMs(createScale(10_000, 1000, 8))).toBe(100);
  expect(rulerStepMs(createScale(600_000, 1000, 1))).toBe(60_000);
});

test("ruler ticks start at zero, label every step and put small marks between", () => {
  const ticks = rulerTicks(createScale(5000, 360, 1));
  expect(ticks[0]).toEqual({ ms: 0, major: true });
  expect(ticks.filter((tick) => tick.major).map((tick) => tick.ms)).toEqual([0, 1000, 2000, 3000, 4000, 5000]);
  expect(ticks.find((tick) => tick.ms === 200)?.major).toBe(false);
});

test("overlapping regions stack into lanes and free lanes are reused", () => {
  const scale = createScale(10_000, 1000, 1);
  const lanes = assignLanes(
    [
      { id: "a", start_ms: 1000, end_ms: 4000 },
      { id: "b", start_ms: 2000, end_ms: 3000 },
      { id: "c", start_ms: 4500, end_ms: 6000 },
      { id: "dot", start_ms: 2500, end_ms: 2500 },
    ],
    scale,
  );
  expect(lanes.get("a")).toBe(0);
  expect(lanes.get("b")).toBe(1);
  expect(lanes.get("dot")).toBe(2);
  expect(lanes.get("c")).toBe(0);
});

test("a dot counts as wide as its mark when it is laid out", () => {
  const scale = createScale(10_000, 1000, 1);
  const lanes = assignLanes(
    [
      { id: "a", start_ms: 1000, end_ms: 1000 },
      { id: "b", start_ms: 1050, end_ms: 1050 },
    ],
    scale,
  );
  expect(lanes.get("b")).toBe(1);
});

test("a dot is laid out where it is drawn: seven pixels either side of its time", () => {
  const scale = createScale(10_000, 1000, 1);
  // The range ends at 96 px. The dot at 100 px covers 93 to 107, so it needs its own lane.
  const lanes = assignLanes(
    [
      { id: "range", start_ms: 200, end_ms: 960 },
      { id: "dot", start_ms: 1000, end_ms: 1000 },
    ],
    scale,
  );
  expect(lanes.get("range")).toBe(0);
  expect(lanes.get("dot")).toBe(1);
  // Eight pixels further on they no longer touch.
  const apart = assignLanes(
    [
      { id: "range", start_ms: 200, end_ms: 860 },
      { id: "dot", start_ms: 1000, end_ms: 1000 },
    ],
    scale,
  );
  expect(apart.get("dot")).toBe(0);
});
