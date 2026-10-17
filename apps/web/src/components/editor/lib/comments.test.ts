import { expect, test } from "vitest";
import { SAMPLE_COMMENTS, SAMPLE_VERSIONS } from "../sample-data";
import type { ReviewAnnotation } from "../types";
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

test("a comment made on a newer version is not on an earlier one yet: it is not mapped backwards", () => {
  const logo = find(resolve(v1), "ann_logo");
  expect(logo.mapped).toBeNull();
  expect(logo.status).toEqual({ kind: "newer", version: 3 });
  expect(statusLabel(logo.status)).toBe("Made on v3 (newer)");
  // Not "no longer mapped", and not open on that version.
  expect(filterComments(resolve(v1), "open").map((item) => item.comment.id)).not.toContain("ann_logo");
  expect(find(resolve(v2), "ann_hook").status).toEqual({ kind: "open" });
  expect(find(resolve(v2), "ann_logo").status.kind).toBe("newer");
});

test("an agent reply to a newer comment is newer too", () => {
  const items = resolveComments(
    [
      { ...SAMPLE_COMMENTS[0], id: "root", version: 2, status: "open" },
      { ...SAMPLE_COMMENTS[1], id: "reply", version: 3, reply_to: "root" },
    ] as ReviewAnnotation[],
    SAMPLE_VERSIONS,
    v1,
  );
  expect(items.map((item) => item.status.kind)).toEqual(["newer", "newer"]);
});

const base: ReviewAnnotation = {
  id: "x",
  version: 3,
  start_ms: 1000,
  end_ms: 1000,
  kind: "comment",
  body: "b",
  status: "open",
  author: { kind: "creator", name: "Sam" },
  created_at: "2026-10-15T09:00:00Z",
};

test("only an open comment counts as open: resolved and applied do not", () => {
  const items = resolveComments(
    [
      { ...base, id: "open" },
      { ...base, id: "resolved", status: "resolved" },
      { ...base, id: "resolved_v", status: "resolved", applied_in_version: 3 },
      { ...base, id: "applied", status: "applied" },
      { ...base, id: "addressed", status: "applied", applied_in_version: 3 },
    ],
    SAMPLE_VERSIONS,
    v3,
  );
  const kinds = Object.fromEntries(items.map((item) => [item.comment.id, item.status]));
  expect(kinds).toEqual({
    open: { kind: "open" },
    resolved: { kind: "resolved" },
    resolved_v: { kind: "resolved" },
    applied: { kind: "applied" },
    addressed: { kind: "addressed", version: 3 },
  });
  expect(statusLabel({ kind: "resolved" })).toBe("Resolved");
  expect(statusLabel({ kind: "applied" })).toBe("Applied");
  expect(countOpen(items)).toBe(1);
  expect(filterComments(items, "open").map((item) => item.comment.id)).toEqual(["open"]);
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
