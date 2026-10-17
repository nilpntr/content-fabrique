import type { MainTimeMapEntry, MsRange, TimeMapEntry, VideoItem } from "../types";

export function isMainEntry(entry: TimeMapEntry): entry is MainTimeMapEntry {
  return "asset_id" in entry;
}

/** `round((out − in) / speed)`, the output duration of a main-track item (spec §2). */
export function itemOutputDurationMs(item: VideoItem): number {
  return Math.round((item.source.out_ms - item.source.in_ms) / item.speed);
}

/** The `time_map` the renderer would write for these items. Used by the sample data and the tests. */
export function buildTimeMap(items: VideoItem[]): MainTimeMapEntry[] {
  return items.map((item) => ({
    out_start_ms: item.at_ms,
    out_end_ms: item.at_ms + itemOutputDurationMs(item),
    item_id: item.id,
    asset_id: item.asset_id,
    source_in_ms: item.source.in_ms,
    speed: item.speed,
  }));
}

/** Where one item ends and the next begins: every main item's start except the first. */
export function cutTimes(items: VideoItem[]): number[] {
  const starts = items.map((item) => item.at_ms).filter((ms) => ms > 0);
  return [...new Set(starts)].sort((a, b) => a - b);
}

/** The main-track entry on screen at output time `t` (a comment at `t` is "linked" to its item). */
export function entryAt(timeMap: TimeMapEntry[], t: number): MainTimeMapEntry | undefined {
  return locate(timeMap.filter(isMainEntry), t, "start");
}

function locate(entries: MainTimeMapEntry[], t: number, edge: "start" | "end"): MainTimeMapEntry | undefined {
  const inside = (entry: MainTimeMapEntry) =>
    edge === "start" ? t >= entry.out_start_ms && t < entry.out_end_ms : t > entry.out_start_ms && t <= entry.out_end_ms;
  const found = entries.find(inside);
  if (found || edge === "end") return found;
  // The very last instant of the render belongs to the last item.
  const last = entries.reduce<MainTimeMapEntry | undefined>(
    (best, entry) => (!best || entry.out_end_ms > best.out_end_ms ? entry : best),
    undefined,
  );
  return last && t === last.out_end_ms ? last : undefined;
}

/** Where `t` falls in the item's source: `source_in_ms + (t − out_start_ms) × speed` (spec §6). */
export function sourceTimeAt(entry: MainTimeMapEntry, t: number): number {
  return entry.source_in_ms + (t - entry.out_start_ms) * entry.speed;
}

export type UnmappedReason = "outside_render" | "item_gone" | "trimmed_out" | "reordered";

export type MapResult =
  | { mapped: true; start_ms: number; end_ms: number; item_id: string }
  | { mapped: false; reason: UnmappedReason };

/**
 * Moves a comment from one render's output time to another's, through the item and its source time.
 *
 * The comment's start (and end, for a range) is resolved to the main item that covers it in `fromTimeMap`, then to a
 * source time. The same item in `toTimeMap` gives the new output time. A comment is unmapped when:
 * - it falls outside the old render (`outside_render`);
 * - the item is no longer on the timeline (`item_gone`);
 * - the item is still there but the moment was trimmed out of it (`trimmed_out`);
 * - a range ends up backwards, because its items were reordered (`reordered`).
 */
export function mapCommentToVersion(comment: MsRange, fromTimeMap: TimeMapEntry[], toTimeMap: TimeMapEntry[]): MapResult {
  const from = fromTimeMap.filter(isMainEntry);
  const to = toTimeMap.filter(isMainEntry);

  const start = place(from, to, comment.start_ms, "start");
  if (!start.ok) return { mapped: false, reason: start.reason };
  if (comment.end_ms <= comment.start_ms) return { mapped: true, start_ms: start.t, end_ms: start.t, item_id: start.item_id };

  const end = place(from, to, comment.end_ms, "end");
  if (!end.ok) return { mapped: false, reason: end.reason };
  if (end.t <= start.t) return { mapped: false, reason: "reordered" };
  return { mapped: true, start_ms: start.t, end_ms: end.t, item_id: start.item_id };
}

type Placed = { ok: true; t: number; item_id: string } | { ok: false; reason: UnmappedReason };

function place(from: MainTimeMapEntry[], to: MainTimeMapEntry[], t: number, edge: "start" | "end"): Placed {
  const old = locate(from, t, edge);
  if (!old) return { ok: false, reason: "outside_render" };
  const target = to.find((entry) => entry.item_id === old.item_id);
  if (!target) return { ok: false, reason: "item_gone" };

  const moved = outputTimeOf(target, sourceTimeAt(old, t));
  return moved === null ? { ok: false, reason: "trimmed_out" } : { ok: true, t: moved, item_id: target.item_id };
}

/** The output time of source time `source` inside `entry`, or null when that part of the source was trimmed away. */
function outputTimeOf(entry: MainTimeMapEntry, source: number): number | null {
  const out = entry.out_start_ms + (source - entry.source_in_ms) / entry.speed;
  // One millisecond of slack for the rounding in the output durations.
  if (out < entry.out_start_ms - 1 || out > entry.out_end_ms + 1) return null;
  return Math.round(Math.min(Math.max(out, entry.out_start_ms), entry.out_end_ms));
}
