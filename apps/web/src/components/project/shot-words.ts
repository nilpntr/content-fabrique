/**
 * The words of the shot list of a proof edit (docs/design/proof-edit-v1.md): plain words for a creator, no ids or milliseconds.
 * Tested in shot-words.test.ts.
 */
import type { ClipFeedback, Shot } from "@/lib/api/client";

/** A second of a clip as people read it: "0:12", "1:05". */
export function clipTime(ms: number): string {
  const total = Math.max(0, Math.floor(ms / 1000));
  return `${Math.floor(total / 60)}:${String(total % 60).padStart(2, "0")}`;
}

/** "Squat · from 0:12": the shot's clip and the second of it the shot starts at. */
export function shotLine(shot: Pick<Shot, "asset_title" | "source_in_ms">): string {
  return `${shot.asset_title?.trim() || "A clip that was removed"} · from ${clipTime(shot.source_in_ms)}`;
}

/** The offers of "Start later", in seconds. */
export const START_LATER_SECONDS = [1, 2, 3, 5] as const;

/** What a piece of feedback says, as the creator would say it. */
export function feedbackWords(feedback: Pick<ClipFeedback, "kind" | "start_ms" | "end_ms">): string {
  if (feedback.kind === "start_later") {
    const seconds = Math.round((feedback.end_ms - feedback.start_ms) / 100) / 10;
    return `Starts ${seconds} s later from now on`;
  }
  return `Won't be used again (${clipTime(feedback.start_ms)} to ${clipTime(feedback.end_ms)})`;
}

/** The line under the heading: what the numbers are for. */
export const SHOTS_HINT = "The numbers match the small numbers at the top right of the video. What you say here is remembered for this clip in every video from now on.";
