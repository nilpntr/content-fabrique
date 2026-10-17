import type { Pin, ReviewAnnotation } from "@/components/editor/types";
import type { Annotation } from "@/lib/api/client";

/** The review screen's own ids, until the API issues a TypeID: a comment with one is on its way to the server. */
export const LOCAL_ID_PREFIX = "ann_local_";
export const isLocalCommentId = (id: string) => id.startsWith(LOCAL_ID_PREFIX);

export const EDITOR_NAME = "Editor";
export const YOU = "You";

/** A point on the frame. The screen pins points; a box (which another client may have made) is shown at its centre. */
function toPin(pin: Annotation["pin"]): Pin | undefined {
  if (!pin) return undefined;
  const w = typeof pin.w === "number" ? pin.w : 0;
  const h = typeof pin.h === "number" ? pin.h : 0;
  return { x: Math.min(1, pin.x + w / 2), y: Math.min(1, pin.y + h / 2) };
}

/**
 * A comment of the API as the review screen has it. `me` is the signed-in user's id: their own comments are "You", a
 * teammate's are "Teammate" (the API names no one), the editor's are "Editor". Until `me` is known a person's comment is "Creator".
 * A comment on a clip (no version) is not a review comment: undefined.
 */
export function toReviewAnnotation(a: Annotation, me?: string): ReviewAnnotation | undefined {
  if (a.timeline_version === null) return undefined;
  const agent = a.author.kind === "agent";
  // A placeholder (a comment this tab is still saving) was written by the signed-in user, whether or not /v1/me has answered yet.
  const own = !agent && (isLocalCommentId(a.id) || (me !== undefined && a.author.id === me));
  const pin = toPin(a.pin);
  return {
    id: a.id,
    version: a.timeline_version,
    start_ms: a.start_ms,
    end_ms: a.end_ms,
    ...(pin ? { pin } : {}),
    kind: a.kind,
    body: a.body,
    status: a.status,
    author: { kind: agent ? "agent" : "creator", name: agent ? EDITOR_NAME : own ? YOU : me === undefined ? "Creator" : "Teammate", ...(own ? { own: true } : {}) },
    ...(a.parent_id ? { reply_to: a.parent_id } : {}),
    ...(a.addressed_in_version !== null ? { applied_in_version: a.addressed_in_version } : {}),
    created_at: a.created_at,
  };
}

export function toReviewAnnotations(list: readonly Annotation[], me?: string): ReviewAnnotation[] {
  return list.flatMap((a) => {
    const mapped = toReviewAnnotation(a, me);
    return mapped ? [mapped] : [];
  });
}
