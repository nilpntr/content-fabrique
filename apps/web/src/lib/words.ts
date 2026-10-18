/**
 * The app's words for a creator who has never heard of runs, renders, checks or tokens. Strings that turn API data into plain
 * language live here (and in `components/project/plain-words.ts` and `storyboard.ts` for the project page), so they are tested in
 * one place and can be translated later without hunting through components.
 */
import type { ProjectStatus, ProjectSummary } from "@/lib/api/client";
import type { Credits } from "@/lib/credits";

// ---- Credits as videos -------------------------------------------------------------------------------------------------------

/**
 * Whole videos the credits pay for. A video is a first edit and two rounds of changes; what that is in credits is the price list's
 * (docs/design/spend-control-v1.md section 3) and comes from the API as `credits_per_video`, never from a number in the app.
 */
export const videosIn = (credits: number, creditsPerVideo: number): number => Math.floor(Math.max(credits, 0) / creditsPerVideo);

export const videoCount = (n: number): string => `${n} ${n === 1 ? "video" : "videos"}`;

/** Whether the balance renews in periods (a monthly allowance), so "this month" is true to say. */
const renews = (credits: Credits): boolean => credits.allowance > 0 && credits.period_end !== null;

/**
 * The balance in a sentence: "≈ 4 videos left this month", "Enough for a few changes" (credits for a round of changes but not a
 * new video) or "No videos left". The number is approximate on purpose: changes cost less than a new video.
 */
export function videosLeftText(credits: Credits): string {
  const videos = videosIn(credits.available, credits.credits_per_video);
  if (credits.available === 0) return "No videos left";
  if (videos === 0) return "Enough for a few changes";
  return `≈ ${videoCount(videos)} left${renews(credits) ? " this month" : ""}`;
}

/** The short form for the header: "4 videos", "<1 video", "0 videos". */
export function videosShort(credits: Credits): string {
  const videos = videosIn(credits.available, credits.credits_per_video);
  if (videos === 0 && credits.available > 0) return "<1 video";
  return videoCount(videos);
}

// ---- Projects ------------------------------------------------------------------------------------------------------------------

export type StatusTone = "neutral" | "busy" | "ready" | "attention";

export interface PlainStatus {
  label: string;
  tone: StatusTone;
}

const STATUS_WORDS: Readonly<Record<ProjectStatus, PlainStatus>> = {
  draft: { label: "Not edited yet", tone: "neutral" },
  editing: { label: "Editing…", tone: "busy" },
  review: { label: "Ready to watch", tone: "ready" },
  approved: { label: "Approved", tone: "ready" },
  exported: { label: "Exported", tone: "neutral" },
};

/**
 * Where a project is, in the creator's words: "Ready to watch", "Editing…", "Needs your footage". A draft without any usable clip
 * needs footage; a status this build does not know reads as it is.
 */
export function projectStatusWords(project: Pick<ProjectSummary, "status" | "asset_count" | "current_timeline_version">): PlainStatus {
  if (project.status === "draft" && project.asset_count === 0) return { label: "Needs your footage", tone: "attention" };
  if (project.status === "draft" && project.current_timeline_version) return { label: "Draft", tone: "neutral" };
  return STATUS_WORDS[project.status as ProjectStatus] ?? { label: project.status, tone: "neutral" };
}

/** "Version 2". The app never says "v2" to a creator. */
export const versionName = (version: number): string => `Version ${version}`;

// ---- Home ----------------------------------------------------------------------------------------------------------------------

/** "Good morning" before noon, "Good afternoon" until six, then "Good evening"; with a first name when there is one. */
export function greeting(now: Date, firstName?: string | null): string {
  const hour = now.getHours();
  const part = hour < 12 ? "Good morning" : hour < 18 ? "Good afternoon" : "Good evening";
  const name = firstName?.trim();
  return name ? `${part}, ${name}` : part;
}
