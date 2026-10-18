/**
 * The project page in plain words: what the editor is doing while it works, how its self-review went, why it stopped, and what
 * changed in a version. The editor speaks as "I". None of these strings names a tool, a step, a check id or a code; those stay
 * in the page's Details.
 */
import type { Run, RunEvent } from "@/lib/api/client";
import type { VideoItem } from "@/components/editor/types";
import { applicable, type CheckRound } from "./run-artifacts";
import { checkName } from "./run-checks";

// ---- While editing -------------------------------------------------------------------------------------------------------------

/** The four stages of an edit as the creator sees them, in order. */
export const EDIT_STAGES = [
  { id: "watch", label: "Watching your clips" },
  { id: "pick", label: "Picking the best reps" },
  { id: "cut", label: "Cutting to the music" },
  { id: "check", label: "Checking the result" },
] as const;

export type EditStage = (typeof EDIT_STAGES)[number]["id"];

/** Which stage a tool belongs to. A tool this build does not know keeps the stage the edit is in. */
const STAGE_OF_TOOL: Readonly<Record<string, EditStage>> = {
  get_brief: "watch",
  get_comments: "watch",
  list_footage: "watch",
  view_clip: "watch",
  find_moments: "pick",
  plan_edit: "pick",
  get_timeline: "cut",
  edit_timeline: "cut",
  match_exposure: "cut",
  render_preview: "check",
  check_edit: "check",
  finish: "check",
};

export interface EditProgress {
  /** Index into {@link EDIT_STAGES}; -1 before the editor has started. */
  stage: number;
  /** One calm line about now: "Cutting to the music…". */
  headline: string;
  /** The editor went back to an earlier stage after checking: it is fixing what it found. */
  polishing: boolean;
}

/** Where an edit is, from the run's events: the stage of its newest tool call, and whether it has been further already. */
export function editProgress(run: Pick<Run, "status" | "events">): EditProgress {
  if (run.status === "waiting_input") return { stage: currentStage(run.events), headline: "I have a question for you", polishing: false };
  if (run.status === "queued") return { stage: -1, headline: "Getting ready…", polishing: false };
  const stage = currentStage(run.events);
  const furthest = furthestStage(run.events);
  const polishing = stage >= 0 && stage < furthest;
  const last = [...run.events].reverse().find((event) => event.type === "tool_call");
  if (stage < 0) return { stage, headline: "Getting ready…", polishing };
  if (last?.tool === "finish") return { stage, headline: "Almost done…", polishing: false };
  if (polishing) return { stage, headline: "Fixing a few things I noticed…", polishing };
  return { stage, headline: `${EDIT_STAGES[stage]?.label ?? "Working"}…`, polishing };
}

function stagesOf(events: readonly RunEvent[]): number[] {
  return events.flatMap((event) => {
    if (event.type !== "tool_call" || !event.tool) return [];
    const stage = STAGE_OF_TOOL[event.tool];
    return stage ? [EDIT_STAGES.findIndex((candidate) => candidate.id === stage)] : [];
  });
}

const currentStage = (events: readonly RunEvent[]): number => stagesOf(events).at(-1) ?? -1;
const furthestStage = (events: readonly RunEvent[]): number => Math.max(-1, ...stagesOf(events));

/** Whether a stage is done, the one the edit is on, or still to come. Stages before the furthest one reached stay done. */
export function stageState(index: number, progress: EditProgress, furthest: number): "done" | "current" | "todo" {
  if (index === progress.stage) return "current";
  if (index < Math.max(progress.stage, furthest)) return "done";
  return "todo";
}

export const furthestStageOf = (run: Pick<Run, "events">): number => furthestStage(run.events);

// ---- When it stopped -----------------------------------------------------------------------------------------------------------

const STOPPED: Readonly<Record<string, string>> = {
  budget: "This edit was bigger than I could finish in one go.",
  daily_cap: "Editing is paused for today because a lot of videos were made. It starts again tomorrow.",
  request_cap: "It's very busy right now, so I had to stop.",
  model_error: "Something went wrong on my side.",
  model_access: "Editing isn't available right now.",
  model_outcome_unknown: "I lost my connection halfway through.",
  refusal: "I can't make a video from these instructions. Try describing it another way.",
  too_many_resumes: "I kept getting interrupted.",
  context_window_exceeded: "There was too much to keep track of. Try again with fewer clips.",
  internal_error: "Something went wrong on our side.",
};

/** Why an edit stopped, in a sentence. Nothing was charged for it (a failed edit is released), which the screen says separately. */
export const stoppedMessage = (reason: string | null | undefined): string => (reason ? STOPPED[reason] : undefined) ?? "I stopped before I could finish.";

/** Whether trying again can help: a refusal needs other instructions, a daily cap needs tomorrow. */
export const canRetry = (reason: string | null | undefined): boolean => reason !== "refusal" && reason !== "daily_cap" && reason !== "model_access";

// ---- The self-review -----------------------------------------------------------------------------------------------------------

export interface ReviewItem {
  name: string;
  /** `fixed`: it failed in an earlier round and passes now. */
  state: "good" | "fixed" | "not_yet";
}

export interface ReviewSummary {
  /** "I checked 13 things — all good", "I fixed 1 thing before showing you this". */
  line: string;
  tone: "good" | "fixed" | "not_yet";
  /** Every check that applied, in friendly names, for the one-click details. */
  items: ReviewItem[];
}

const things = (n: number): string => `${n} ${n === 1 ? "thing" : "things"}`;

/**
 * The editor's self-review in one line, from its rounds of checks: the last round says where the video is, the earlier ones what
 * was fixed on the way. Undefined when the editor did not check its work.
 */
export function reviewSummary(rounds: readonly CheckRound[]): ReviewSummary | undefined {
  const last = rounds.at(-1);
  if (!last) return undefined;
  const failedBefore = new Set(rounds.slice(0, -1).flatMap((round) => round.checks.filter((check) => check.status === "fail").map((check) => check.id)));
  const items: ReviewItem[] = last.checks
    .filter((check) => check.status !== "skip")
    .map((check) => ({ name: checkName(check.id), state: check.status === "fail" ? "not_yet" : failedBefore.has(check.id) ? "fixed" : "good" }));
  const total = applicable(last);
  // An earlier round known only from its cut-off summary may not name what failed; its count still says something was fixed.
  const earlierFailed = Math.max(0, ...rounds.slice(0, -1).map((round) => round.failed));
  const fixed = Math.max(items.filter((item) => item.state === "fixed").length, last.failed === 0 ? earlierFailed : 0);
  if (last.failed > 0) return { line: `I checked ${things(total)}. ${last.failed === 1 ? "1 could" : `${last.failed} could`} still be better.`, tone: "not_yet", items };
  if (fixed > 0) return { line: `I fixed ${things(fixed)} before showing you this`, tone: "fixed", items };
  return { line: `I checked ${things(total)} — all good`, tone: "good", items };
}

// ---- What changed in a version -------------------------------------------------------------------------------------------------

/**
 * A line of a version's change list with the timeline's item ids ("c4") named by their clip ("Seated cable row"). Ids that are not
 * items of the version are left as they are.
 */
export function humanizeChange(line: string, items: readonly Pick<VideoItem, "id" | "asset_id">[], assetNames: Readonly<Record<string, string>>): string {
  const names = new Map(items.flatMap((item) => (assetNames[item.asset_id] ? [[item.id, assetNames[item.asset_id] as string] as const] : [])));
  return line.replace(/\b[a-z]{1,3}\d+\b/g, (id) => names.get(id) ?? id);
}
