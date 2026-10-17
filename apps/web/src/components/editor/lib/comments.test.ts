import { expect, test } from "vitest";
import { SAMPLE_COMMENTS, SAMPLE_VERSIONS } from "../sample-data";
import { countOpen, filterComments, resolveComments, statusLabel } from "./comments";

const [v1, v2, v3] = SAMPLE_VERSIONS;
if (!v1 || !v2 || !v3) throw new Error("sample data needs three versions");

const resolve = (viewed = v3) => resolveComments(SAMPLE_COMMENTS, SAMPLE_VERSIONS, viewed);
const find = (items: ReturnType<typeof resolve>, id: string) => {
  const item = items.find((candidate) => candidate.comment.id === id);
  if (!item) throw new Error(`no comment ${id}`);
  return item;
};

test("a comment made on the viewed version keeps its range and is linked to its clip", () => {
  const logo = find(resolve(), "ann_logo");
  expect(logo.mapped).toEqual({ start_ms: 2600, end_ms: 2600, item_id: "c1" });
  expect(logo.status).toEqual({ kind: "open" });
});

test("a comment from v1 about a removed clip is no longer mapped on v3, and says it was addressed in v2", () => {
  const pulldown = find(resolve(), "ann_pulldown");
  expect(pulldown.mapped).toBeNull();
  expect(pulldown.status).toEqual({ kind: "unmapped", reason: "item_gone" });
  expect(pulldown.addressedIn).toBe(2);
  expect(statusLabel(pulldown.status)).toBe("No longer mapped");
});

test("the same comment is mapped, and addressed, on the version it was made on", () => {
  const pulldown = find(resolve(v1), "ann_pulldown");
  expect(pulldown.mapped).toEqual({ start_ms: 4500, end_ms: 6500, item_id: "c2" });
  expect(pulldown.status).toEqual({ kind: "addressed", version: 2 });
  expect(statusLabel(pulldown.status)).toBe("Addressed in v2");
});

test("a comment from v1 maps onto v3 through item and source time", () => {
  // v1 9000 to 11000 is c3's source 3000 to 5000. In v3 c3 starts at 3500 at 1.25x.
  const text = find(resolve(), "ann_text");
  expect(text.mapped).toEqual({ start_ms: 4300, end_ms: 5900, item_id: "c3" });
});

test("a comment from v2 maps through a speed change", () => {
  expect(find(resolve(), "ann_pace").mapped).toMatchObject({ start_ms: 5500, end_ms: 6700 });
});

test("comments made on later versions map back onto an earlier one", () => {
  // v3 2600 is c1 source 4100, which is 3100 ms into v1's c1.
  expect(find(resolve(v1), "ann_logo").mapped).toMatchObject({ start_ms: 3100, item_id: "c1" });
});

test("an agent reply follows the comment it answers", () => {
  const reply = find(resolve(), "ann_pulldown_reply");
  expect(reply.isReply).toBe(true);
  expect(reply.rootId).toBe("ann_pulldown");
  expect(reply.mapped).toBeNull();
  expect(find(resolve(), "ann_text_reply").mapped).toEqual(find(resolve(), "ann_text").mapped);
});

test("the open filter keeps open threads, newest first, and the all filter keeps everything", () => {
  const items = resolve();
  const open = filterComments(items, "open").map((item) => item.comment.id);
  expect(open).toEqual(["ann_logo", "ann_broll", "ann_hook"]);
  expect(countOpen(items)).toBe(3);

  const all = filterComments(items, "all").map((item) => item.comment.id);
  expect(all).toHaveLength(SAMPLE_COMMENTS.length);
  expect(all[0]).toBe("ann_logo");
  expect(all.indexOf("ann_text_reply")).toBeLessThan(all.indexOf("ann_text"));
});

test("an unmapped comment is not open, and its reply leaves the open list with it", () => {
  const open = filterComments(resolve(), "open").map((item) => item.comment.id);
  expect(open).not.toContain("ann_pulldown");
  expect(open).not.toContain("ann_pulldown_reply");
});
