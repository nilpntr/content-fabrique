import type { ReviewAnnotation, ReviewVersion } from "../types";
import { entryAt, type MapResult, mapCommentToVersion, type UnmappedReason } from "./time-map";

export type CommentStatus =
  | { kind: "open" }
  | { kind: "addressed"; version: number }
  | { kind: "unmapped"; reason: UnmappedReason };

export interface MappedRange {
  start_ms: number;
  end_ms: number;
  item_id: string;
}

/** A comment as the side panel and the timeline show it, relative to the version being viewed. */
export interface ResolvedComment {
  comment: ReviewAnnotation;
  /** The thread's first comment: the comment itself, or the one an agent reply answers. */
  rootId: string;
  isReply: boolean;
  /** Where the comment sits on the viewed version, or null when it no longer maps. */
  mapped: MappedRange | null;
  status: CommentStatus;
  /** Set when the editor already applied it, even if the status shows "no longer mapped". */
  addressedIn?: number;
}

export type CommentFilter = "open" | "all";

function toMapped(result: MapResult): MappedRange | null {
  return result.mapped ? { start_ms: result.start_ms, end_ms: result.end_ms, item_id: result.item_id } : null;
}

/**
 * Puts every comment onto `viewed`'s render. A comment made on another version goes through `time_map`
 * (`mapCommentToVersion`). An agent reply follows the comment it answers.
 */
export function resolveComments(comments: ReviewAnnotation[], versions: ReviewVersion[], viewed: ReviewVersion): ResolvedComment[] {
  const byId = new Map(comments.map((comment) => [comment.id, comment]));
  const mapOf = (version: number) => versions.find((candidate) => candidate.version === version)?.time_map ?? [];

  const mapRoot = (root: ReviewAnnotation): MapResult =>
    root.version === viewed.version
      ? { mapped: true, start_ms: root.start_ms, end_ms: root.end_ms, item_id: entryAt(viewed.time_map, root.start_ms)?.item_id ?? "" }
      : mapCommentToVersion(root, mapOf(root.version), viewed.time_map);

  return comments.map((comment) => {
    const root = (comment.reply_to && byId.get(comment.reply_to)) || comment;
    const result = mapRoot(root);
    const addressedIn = root.status !== "open" ? root.applied_in_version : undefined;

    let status: CommentStatus = { kind: "open" };
    if (!result.mapped) status = { kind: "unmapped", reason: result.reason };
    else if (addressedIn !== undefined) status = { kind: "addressed", version: addressedIn };

    return {
      comment,
      rootId: root.id,
      isReply: root !== comment,
      mapped: toMapped(result),
      status,
      addressedIn: status.kind === "unmapped" ? addressedIn : undefined,
    };
  });
}

export function statusLabel(status: CommentStatus): string {
  switch (status.kind) {
    case "open":
      return "Open";
    case "addressed":
      return `Addressed in v${status.version}`;
    case "unmapped":
      return "No longer mapped";
  }
}

const byNewest = (a: ResolvedComment, b: ResolvedComment) =>
  Date.parse(b.comment.created_at) - Date.parse(a.comment.created_at) || b.comment.id.localeCompare(a.comment.id);

/**
 * Newest first. "open" keeps the threads whose comment is still open on this version; an agent reply stays with its
 * thread, so it shows exactly when the comment it answers does.
 */
export function filterComments(items: ResolvedComment[], filter: CommentFilter): ResolvedComment[] {
  const openRoots = new Set(items.filter((item) => item.status.kind === "open").map((item) => item.rootId));
  return items.filter((item) => filter === "all" || openRoots.has(item.rootId)).sort(byNewest);
}

export function countOpen(items: ResolvedComment[]): number {
  return items.filter((item) => !item.isReply && item.status.kind === "open").length;
}
