import { expect, test } from "vitest";
import type { VideoItem } from "../types";
import { buildTimeMap, cutTimes, entryAt, itemOutputDurationMs, mapCommentToVersion } from "./time-map";

const item = (id: string, at: number, source: [number, number], speed = 1): VideoItem => ({
  id,
  asset_id: `ast_${id}`,
  at_ms: at,
  source: { in_ms: source[0], out_ms: source[1] },
  speed,
});

// c1 and c2 play back to back.
const v1 = buildTimeMap([item("c1", 0, [1000, 5000]), item("c2", 4000, [0, 4000])]);

test("an item's output duration is round((out - in) / speed)", () => {
  expect(itemOutputDurationMs(item("c", 0, [1000, 5000]))).toBe(4000);
  expect(itemOutputDurationMs(item("c", 0, [0, 4000], 1.25))).toBe(3200);
  expect(itemOutputDurationMs(item("c", 0, [0, 1000], 3))).toBe(333);
});

test("the time map carries source_in_ms and speed", () => {
  expect(v1[0]).toEqual({ out_start_ms: 0, out_end_ms: 4000, item_id: "c1", asset_id: "ast_c1", source_in_ms: 1000, speed: 1 });
});

test("cuts are every main item's start except the first", () => {
  expect(cutTimes([item("c1", 0, [0, 4000]), item("c2", 4000, [0, 4000]), item("c3", 8000, [0, 4000])])).toEqual([4000, 8000]);
});

test("entryAt finds the item that covers a time, so a comment at 0:04 links to the right clip", () => {
  expect(entryAt(v1, 1200)?.item_id).toBe("c1");
  expect(entryAt(v1, 4000)?.item_id).toBe("c2");
  expect(entryAt(v1, 8000)?.item_id).toBe("c2");
  expect(entryAt(v1, 8001)).toBeUndefined();
});

test("a comment maps through the item and its source time when the item moved", () => {
  // c1 is trimmed by 500 ms at the start and c2 is inserted before it.
  const v2 = buildTimeMap([item("c2", 0, [0, 4000]), item("c1", 4000, [1500, 5000])]);
  // 1200 ms into v1 is 2200 ms into c1's source: 700 ms into c1 in v2, which starts at 4000.
  expect(mapCommentToVersion({ start_ms: 1200, end_ms: 1200 }, v1, v2)).toEqual({ mapped: true, start_ms: 4700, end_ms: 4700, item_id: "c1" });
});

test("a range maps both ends", () => {
  const v2 = buildTimeMap([item("c1", 0, [1500, 5000]), item("c2", 3500, [0, 4000])]);
  expect(mapCommentToVersion({ start_ms: 4500, end_ms: 6500 }, v1, v2)).toEqual({
    mapped: true,
    start_ms: 4000,
    end_ms: 6000,
    item_id: "c2",
  });
});

test("a speed change stretches the mapped range", () => {
  const faster = buildTimeMap([item("c1", 0, [1000, 5000], 2), item("c2", 2000, [0, 4000])]);
  // 1000 to 3000 ms into v1 is source 2000 to 4000, which is 500 to 1500 ms at double speed.
  expect(mapCommentToVersion({ start_ms: 1000, end_ms: 3000 }, v1, faster)).toEqual({ mapped: true, start_ms: 500, end_ms: 1500, item_id: "c1" });
});

test("a range that ends where its item ends maps to the end of the item", () => {
  const slower = buildTimeMap([item("c1", 0, [1000, 5000], 0.5), item("c2", 8000, [0, 4000])]);
  expect(mapCommentToVersion({ start_ms: 2000, end_ms: 4000 }, v1, slower)).toEqual({ mapped: true, start_ms: 4000, end_ms: 8000, item_id: "c1" });
});

test("a comment is unmapped when its item is gone", () => {
  const withoutC2 = buildTimeMap([item("c1", 0, [1000, 5000])]);
  expect(mapCommentToVersion({ start_ms: 4500, end_ms: 6500 }, v1, withoutC2)).toEqual({ mapped: false, reason: "item_gone" });
  expect(mapCommentToVersion({ start_ms: 4500, end_ms: 4500 }, v1, withoutC2)).toEqual({ mapped: false, reason: "item_gone" });
});

test("a comment is unmapped when the moment was trimmed out of its item", () => {
  // c1 now starts at source 3000: the first two seconds of v1's c1 are gone.
  const trimmed = buildTimeMap([item("c1", 0, [3000, 5000]), item("c2", 2000, [0, 4000])]);
  expect(mapCommentToVersion({ start_ms: 500, end_ms: 500 }, v1, trimmed)).toEqual({ mapped: false, reason: "trimmed_out" });
  expect(mapCommentToVersion({ start_ms: 500, end_ms: 1500 }, v1, trimmed)).toEqual({ mapped: false, reason: "trimmed_out" });
});

test("a comment is unmapped when it lies outside the render it was made on", () => {
  expect(mapCommentToVersion({ start_ms: 9000, end_ms: 9000 }, v1, v1)).toEqual({ mapped: false, reason: "outside_render" });
});

test("a range whose items swapped places is unmapped", () => {
  const swapped = buildTimeMap([item("c2", 0, [0, 4000]), item("c1", 4000, [1000, 5000])]);
  expect(mapCommentToVersion({ start_ms: 3000, end_ms: 5000 }, v1, swapped)).toEqual({ mapped: false, reason: "reordered" });
});

test("mapping a version onto itself changes nothing", () => {
  expect(mapCommentToVersion({ start_ms: 2000, end_ms: 5000 }, v1, v1)).toEqual({ mapped: true, start_ms: 2000, end_ms: 5000, item_id: "c1" });
});

test("text items in the time map are ignored", () => {
  const withText = [...v1, { out_start_ms: 0, out_end_ms: 8000, item_id: "t1" }];
  expect(mapCommentToVersion({ start_ms: 1000, end_ms: 1000 }, withText, withText)).toMatchObject({ mapped: true, item_id: "c1" });
});
