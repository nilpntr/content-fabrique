import { expect, test } from "vitest";
import { mainItems } from "../../project/review-data";
import type { VideoItem } from "../types";
import { rampOutMs, rampSourceMs } from "./speed-ramp";
import { buildTimeMap, itemOutputDurationMs, mapCommentToVersion, sourceTimeAt } from "./time-map";

// The same ramp as the Go tests (internal/timeline/ramp_test.go): 2x for a second, then down to 0.5x at 1600 ms of source.
const ramp = [
  { at_ms: 0, speed: 2 },
  { at_ms: 1000, speed: 2 },
  { at_ms: 1600, speed: 0.5 },
];

test("a ramp's output time is the integral of 1 / speed", () => {
  // 1000 ms at 2x is 500; 600 ms from 2x to 0.5x is ln(4) / 0.0025 = 554.5; the last 400 ms at 0.5x is 800.
  expect(rampOutMs(ramp, 1000)).toBeCloseTo(500, 6);
  expect(rampOutMs(ramp, 1600)).toBeCloseTo(1054.5177, 3);
  expect(rampOutMs(ramp, 2000)).toBeCloseTo(1854.5177, 3);
});

test("the source time is the inverse of the output time", () => {
  for (const s of [0, 1, 250, 999, 1000, 1300, 1600, 1900, 2000]) {
    expect(rampSourceMs(ramp, rampOutMs(ramp, s))).toBeCloseTo(s, 6);
  }
});

test("a ramped item's duration and time map follow the ramp", () => {
  const item: VideoItem = { id: "c", asset_id: "ast_c", at_ms: 1000, source: { in_ms: 1000, out_ms: 3000 }, speed: 2000 / 1855, speed_ramp: ramp };
  expect(itemOutputDurationMs(item)).toBe(1855);
  const [entry] = buildTimeMap([item]);
  expect(entry?.speed_ramp).toEqual(ramp);
  // 100 ms into the clip is at 2x: 200 ms of source, not the 107 ms an average speed would give.
  expect(sourceTimeAt(entry as never, 1100)).toBeCloseTo(1200, 6);
});

test("a comment moves through a ramped item by source time", () => {
  const item: VideoItem = { id: "c", asset_id: "ast_c", at_ms: 0, source: { in_ms: 0, out_ms: 2000 }, speed: 2000 / 1855, speed_ramp: ramp };
  // The same footage, with the clip trimmed to start 500 ms later in the source: the ramp is shifted by the editor's op.
  const later: VideoItem = { ...item, source: { in_ms: 500, out_ms: 2000 }, speed_ramp: [{ at_ms: 0, speed: 2 }, { at_ms: 500, speed: 2 }, { at_ms: 1100, speed: 0.5 }] };
  const result = mapCommentToVersion({ start_ms: 400, end_ms: 400 }, buildTimeMap([item]), buildTimeMap([later]));
  // 400 ms of output is 800 ms of source at 2x, which is 300 ms into the later clip, at 2x: 150 ms.
  expect(result).toMatchObject({ mapped: true });
  if (result.mapped) expect(result.start_ms).toBeCloseTo(150, -1);
});

test("the review reads a speed ramp and the new transition types from the spec", () => {
  const spec = {
    tracks: [
      {
        type: "video",
        items: [
          { id: "c1", asset_id: "ast_a", at_ms: 0, source: { in_ms: 0, out_ms: 2000 }, speed_ramp: ramp },
          { id: "c2", asset_id: "ast_b", at_ms: 1555, source: { in_ms: 0, out_ms: 2000 }, transition_in: { type: "zoom_through", duration_ms: 300 } },
        ],
      },
    ],
  };
  const [c1, c2] = mainItems(spec);
  expect(c1?.speed_ramp).toEqual(ramp);
  expect(c1?.speed).toBeCloseTo(2000 / 1854.5177, 4);
  expect(c2?.transition_in).toEqual({ type: "zoom_through", duration_ms: 300 });
});
