import { expect, test } from "vitest";
import { annotation, annotationId, runId } from "@/test-fixtures";
import { isLocalCommentId, toReviewAnnotation, toReviewAnnotations } from "./annotations";

const ME = "usr_01test";

test("a person's comment is theirs or a teammate's, and the editor's is the editor's", () => {
  expect(toReviewAnnotation(annotation(1), ME)?.author).toEqual({ kind: "creator", name: "You", own: true });
  expect(toReviewAnnotation(annotation(1, { author: { kind: "user", id: "usr_01other" } }), ME)?.author).toEqual({ kind: "creator", name: "Teammate" });
  // Until the signed-in user is known, nobody is "You".
  expect(toReviewAnnotation(annotation(1), undefined)?.author).toEqual({ kind: "creator", name: "Creator" });
  // The editor's id is a run, never mistaken for the user.
  expect(toReviewAnnotation(annotation(1, { author: { kind: "agent", id: runId(1) } }), ME)?.author).toEqual({ kind: "agent", name: "Editor" });
  expect(toReviewAnnotation(annotation(1, { author: { kind: "agent", id: ME } }), ME)?.author.own).toBeUndefined();
});

test("the fields of the screen come from the fields of the API", () => {
  const mapped = toReviewAnnotation(
    annotation(2, { timeline_version: 2, start_ms: 4000, end_ms: 6000, kind: "instruction", body: "Bigger", status: "applied", addressed_in_version: 3, parent_id: annotationId(1), pin: { x: 0.25, y: 0.5 } }),
    ME,
  );
  expect(mapped).toEqual({
    id: annotationId(2),
    version: 2,
    start_ms: 4000,
    end_ms: 6000,
    pin: { x: 0.25, y: 0.5 },
    kind: "instruction",
    body: "Bigger",
    status: "applied",
    author: { kind: "creator", name: "You", own: true },
    reply_to: annotationId(1),
    applied_in_version: 3,
    created_at: "2026-10-17T10:02:00Z",
  });
  // No pin, no parent, nothing applied: the optional fields are absent, not null.
  const plain = toReviewAnnotation(annotation(1), ME);
  expect(plain).not.toHaveProperty("pin");
  expect(plain).not.toHaveProperty("reply_to");
  expect(plain).not.toHaveProperty("applied_in_version");
});

test("a box pin is shown at its centre, inside the frame", () => {
  expect(toReviewAnnotation(annotation(1, { pin: { x: 0.5, y: 0.5, w: 0.5, h: 0.2 } }), ME)?.pin).toEqual({ x: 0.75, y: 0.6 });
  expect(toReviewAnnotation(annotation(1, { pin: { x: 1, y: 1, w: 1, h: 1 } }), ME)?.pin).toEqual({ x: 1, y: 1 });
});

test("a comment on a clip is not a review comment", () => {
  const onClip = annotation(1, { target_type: "asset", project_id: null, timeline_version: null });
  expect(toReviewAnnotation(onClip, ME)).toBeUndefined();
  expect(toReviewAnnotations([onClip, annotation(2)], ME).map((c) => c.id)).toEqual([annotationId(2)]);
});

test("the screen's own ids are told from the API's", () => {
  expect(isLocalCommentId("ann_local_3")).toBe(true);
  expect(isLocalCommentId(annotationId(1))).toBe(false);
});
