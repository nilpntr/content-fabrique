import type { ReviewAnnotation, ReviewVersion } from "../types";
import { entryAt, type MapResult, mapCommentToVersion, type UnmappedReason } from "./time-map";

/**
 * What the panel shows for a comment on the version being viewed. Only `open` counts as open.
 * - `addressed`: the editor applied it in a known version. `applied` is the same without a version, `resolved` is closed by hand.
 * - `unmapped`: the moment is gone from this version.
 * - `newer`: made on a later version than the one viewed, so it did not exist yet.
 */
export type CommentStatus =
  | { kind: "open" }
  | { kind: "addressed"; version: number }
  | { kind: "applied" }
  | { kind: "resolved" }
  | { kind: "unmapped"; reason: UnmappedReason }
  | { kind: "newer"; version: number };

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

  const mapRoot = (root: ReviewAnnotation): MapResult | "newer" => {
    // Comments only map forward in time. One made on a later version is not on this one yet.
    if (root.version > viewed.version) return "newer";
    if (root.version === viewed.version) {
      return { mapped: true, start_ms: root.start_ms, end_ms: root.end_ms, item_id: entryAt(viewed.time_map, root.start_ms)?.item_id ?? "" };
    }
    return mapCommentToVersion(root, mapOf(root.version), viewed.time_map);
  };

  return comments.map((comment) => {
    const root = (comment.reply_to && byId.get(comment.reply_to)) || comment;
    const result = mapRoot(root);
    const addressedIn = root.status !== "open" ? root.applied_in_version : undefined;

    let status: CommentStatus;
    if (result === "newer") status = { kind: "newer", version: root.version };
    else if (!result.mapped) status = { kind: "unmapped", reason: result.reason };
    else if (root.status === "open") status = { kind: "open" };
    else if (root.status === "resolved") status = { kind: "resolved" };
    else status = addressedIn === undefined ? { kind: "applied" } : { kind: "addressed", version: addressedIn };

    return {
      comment,
      rootId: root.id,
      isReply: root !== comment,
      mapped: result === "newer" ? null : toMapped(result),
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
      return `Addressed in Version ${status.version}`;
    case "applied":
      return "Applied";
    case "resolved":
      return "Resolved";
    case "unmapped":
      return "No longer mapped";
    case "newer":
      return `Made on Version ${status.version} (newer)`;
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
