import { expect, test } from "vitest";
import { clip, readyRender, timelineVersion, versionSummary } from "@/test-fixtures";
import { mainItems, parseTimeMap, specDurationMs, toReviewVersion, versionSummaryLine } from "./review-data";

const v1 = timelineVersion(1, [clip("c1", 1, 0, 1000, 5000), clip("c2", 2, 4000, 0, 4000, 2)]);

test("the main track's clips are read in time order; anything malformed is left out", () => {
  const spec = {
    tracks: [
      { id: "text1", type: "text", items: [{ id: "t1" }] },
      {
        id: "main",
        type: "video",
        items: [
          { id: "b", asset_id: "ast_b", at_ms: 4000, source: { in_ms: 0, out_ms: 4000 }, transition_in: { type: "crossfade", duration_ms: 300 } },
          { id: "a", asset_id: "ast_a", at_ms: 0, source: { in_ms: 1000, out_ms: 5000 }, speed: 1.5, transition_in: { type: "cut" } },
          { id: "broken", asset_id: "ast_c", at_ms: "soon", source: { in_ms: 0, out_ms: 1 } },
          "nonsense",
        ],
      },
    ],
  };
  const items = mainItems(spec);
  expect(items.map((item) => item.id)).toEqual(["a", "b"]);
  expect(items[0]).toMatchObject({ speed: 1.5, transition_in: { type: "cut" } });
  expect(items[1]).toMatchObject({ speed: 1, transition_in: { type: "crossfade", duration_ms: 300 } });
  expect(mainItems(null)).toEqual([]);
  expect(mainItems({ tracks: "no" })).toEqual([]);
});

test("the length of a cut is where its last clip ends, speed included", () => {
  expect(specDurationMs(mainItems(v1.spec))).toBe(4000 + 2000);
  expect(specDurationMs([])).toBe(0);
});

test("a version without a render has the time map the renderer would write, and a one-line summary", () => {
  const version = toReviewVersion({ summary: versionSummary(1, { summary: ["Added c1", "Added c2"] }), version: v1 }, "9:16");
  expect(version).toMatchObject({ version: 1, duration_ms: 6000, summary: "Added c1; Added c2", output: { format: "9:16", fps: 30 } });
  expect(version?.time_map).toEqual([
    { out_start_ms: 0, out_end_ms: 4000, item_id: "c1", asset_id: expect.any(String), source_in_ms: 1000, speed: 1 },
    { out_start_ms: 4000, out_end_ms: 6000, item_id: "c2", asset_id: expect.any(String), source_in_ms: 0, speed: 2 },
  ]);
  expect(version?.preview_url).toBeUndefined();
  // The spec is not there yet.
  expect(toReviewVersion({ summary: versionSummary(1), version: null }, "9:16")).toBeNull();
});

test("a ready render brings its video, its length and its time map", () => {
  const timeMap = [{ out_start_ms: 0, out_end_ms: 4100, item_id: "c1", asset_id: "ast_x", source_in_ms: 1000, speed: 1 }];
  const version = toReviewVersion({ summary: versionSummary(1), version: v1, render: readyRender({ duration_ms: 6100 }), renderTimeMap: timeMap, previewUrl: "https://files.test/p.mp4" }, "9:16");
  expect(version).toMatchObject({ duration_ms: 6100, preview_url: "https://files.test/p.mp4", time_map: timeMap });
  // A time map with no main entry is no use for comments: the derived one is kept.
  const textOnly = toReviewVersion({ summary: versionSummary(1), version: v1, renderTimeMap: [{ out_start_ms: 0, out_end_ms: 1, item_id: "t1" }] }, "9:16");
  expect(textOnly?.time_map).toHaveLength(2);
});

test("the render's time map file is read defensively", () => {
  expect(
    parseTimeMap([
      { out_start_ms: 0, out_end_ms: 100, item_id: "c1", asset_id: "ast_a", source_in_ms: 5, speed: 2 },
      { out_start_ms: 0, out_end_ms: 50, item_id: "t1" },
      { out_start_ms: "x", out_end_ms: 1, item_id: "bad" },
      null,
    ]),
  ).toEqual([
    { out_start_ms: 0, out_end_ms: 100, item_id: "c1", asset_id: "ast_a", source_in_ms: 5, speed: 2 },
    { out_start_ms: 0, out_end_ms: 50, item_id: "t1" },
  ]);
  expect(parseTimeMap({ not: "an array" })).toBeUndefined();
});

test("a version with no listed changes says who made it", () => {
  expect(versionSummaryLine(versionSummary(2, { summary: [], author_kind: "agent" }))).toBe("Edited by the editor");
  expect(versionSummaryLine(versionSummary(2, { summary: [" "], author_kind: "user" }))).toBe("Edited by you");
});
