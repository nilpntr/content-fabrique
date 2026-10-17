import type { Run, RunEvent } from "@/lib/api/client";
import { answerTo, checkRoundFromSummary, matchFromSummary, planFromSummaries } from "./run-artifacts";

/** What a step is about, which picks its icon in the feed. */
export type StepKind =
  | "brief"
  | "footage"
  | "find_moments"
  | "view_clip"
  | "plan"
  | "timeline"
  | "edit"
  | "match_exposure"
  | "preview"
  | "check"
  | "finish"
  | "question"
  | "working"
  | "note"
  | "error";

/** A line of the run feed in the creator's words. */
export interface RunStep {
  seq: number;
  label: string;
  /** `error` is a step that failed; `note` is something that happened to the run (a limit, a cut-off answer), not a step the editor chose. */
  tone: "normal" | "error" | "note";
  kind: StepKind;
  /** What came of the step, in a few words: "12 of 13 pass". Known once the tool has answered. */
  detail?: string;
  /** The tool has not answered yet. */
  pending?: boolean;
}

const ASSET_ID = /ast_[0-9a-z]{26}/;

/** The `base_version` of an edit_timeline call, from the one-line summary `edit_timeline {"base_version":2,"ops":[...` (cut at 400 bytes, so it is read with a pattern and not parsed). */
function baseVersionOf(text: string): number | undefined {
  const match = /"base_version"\s*:\s*(\d+)/.exec(text);
  return match ? Number(match[1]) : undefined;
}

const KINDS: Readonly<Record<string, StepKind>> = {
  get_brief: "brief",
  list_footage: "footage",
  find_moments: "find_moments",
  view_clip: "view_clip",
  plan_edit: "plan",
  get_timeline: "timeline",
  get_comments: "brief",
  edit_timeline: "edit",
  match_exposure: "match_exposure",
  render_preview: "preview",
  check_edit: "check",
  finish: "finish",
  ask_user: "question",
};

/** The kind of a tool, for its icon. A tool this build does not know is "working". */
export const stepKind = (tool: string | null | undefined): StepKind => (tool ? KINDS[tool] : undefined) ?? "working";

/** What a tool call means for the creator. `titles` maps an asset id to the clip's title. */
export function toolLabel(tool: string | null | undefined, text: string, titles: ReadonlyMap<string, string>): string {
  switch (tool) {
    case "get_brief":
      return "Reading your instructions";
    case "list_footage":
      return "Looking through your footage";
    case "find_moments":
      return "Finding the best moments";
    case "view_clip": {
      const id = ASSET_ID.exec(text)?.[0];
      const title = id ? titles.get(id) : undefined;
      return title ? `Looking at ${title}…` : "Looking at a clip…";
    }
    case "plan_edit":
      return "Planning the edit…";
    case "get_timeline":
      return "Checking the timeline…";
    case "get_comments":
      return "Reading your comments";
    case "edit_timeline": {
      const base = baseVersionOf(text);
      return base === undefined ? "Editing the timeline…" : `Editing the timeline (v${base + 1})…`;
    }
    case "match_exposure":
      return "Evening out the brightness…";
    case "render_preview":
      return "Rendering a preview…";
    case "check_edit":
      return "Checking its own work…";
    case "finish":
      return "Writing up the result…";
    case "ask_user":
      return "Asking you a question…";
    default:
      return "Working…";
  }
}

/** What a finished tool call came to, in a few words; undefined when the answer says nothing worth a line. */
export function stepDetail(tool: string | null | undefined, answer: string): string | undefined {
  switch (tool) {
    case "check_edit": {
      const round = checkRoundFromSummary(answer, 1);
      if (!round) return undefined;
      const total = round.passed + round.failed;
      return round.failed > 0 ? `${round.passed} of ${total} pass, ${round.failed} to fix` : `${round.passed} of ${total} pass`;
    }
    case "plan_edit": {
      const plan = planFromSummaries(undefined, answer);
      if (!plan) return undefined;
      const parts = [plan.contentType.replaceAll("_", " ")];
      if (plan.tempoBpm) parts.push(`${plan.tempoBpm} BPM`);
      if (plan.shotCount) parts.push(`${plan.shotCount} shots`);
      return parts.join(" · ");
    }
    case "match_exposure": {
      const match = matchFromSummary(answer);
      if (!match) return undefined;
      return match.clips === 0 ? "already even" : `${match.clips} ${match.clips === 1 ? "clip" : "clips"} evened out`;
    }
    default:
      return undefined;
  }
}

/** The steps that are not tool calls: a limit that was reached, an answer that was cut off. A status the feed does not show is null. */
function noteOf(event: RunEvent): RunStep | null {
  if (event.type !== "status") return null;
  const limit = /\blimit=(\w+)/.exec(event.text)?.[1];
  if (/^limit_reached\b/.test(event.text)) return { seq: event.seq, kind: "note", tone: "note", label: `The editor reached ${limit ? (LIMITS[limit] ?? "a limit") : "a limit"} and is wrapping up` };
  if (/^succeeded\b/.test(event.text) && limit) return { seq: event.seq, kind: "note", tone: "note", label: `Finished early, at ${LIMITS[limit] ?? "a limit"}` };
  if (/^cut_off\b/.test(event.text)) return { seq: event.seq, kind: "note", tone: "note", label: "The editor's answer was cut off, so it tried again" };
  return null;
}

/** The step an event stands for, or null for the events the feed does not show (the model's own messages, results that went well). */
export function describeEvent(event: RunEvent, titles: ReadonlyMap<string, string>): RunStep | null {
  if (event.type === "tool_call") return { seq: event.seq, label: toolLabel(event.tool, event.text, titles), tone: "normal", kind: stepKind(event.tool) };
  if (event.type === "tool_result" && event.is_error) return { seq: event.seq, label: "A step did not go through; the editor is adjusting", tone: "error", kind: "error" };
  if (event.type === "error") return { seq: event.seq, label: event.text || "Something went wrong", tone: "error", kind: "error" };
  return noteOf(event);
}

const withoutEllipsis = (label: string): string => label.replace(/…$/, "");

/** Whether a later event answers (or fails) a call of `tool`: the editor runs one call at a time, so any later result of the tool is the call's. */
function hasLaterResult(events: readonly RunEvent[], index: number, tool: string | null | undefined): boolean {
  for (let i = index + 1; i < events.length; i++) {
    const later = events[i];
    if (later?.type === "tool_result" && later.tool === tool) return true;
  }
  return false;
}

/** "Row 1, Row 2 and 3 more". */
function nameList(names: readonly string[]): string {
  if (names.length <= 3) return names.length === 3 ? `${names[0]}, ${names[1]} and ${names[2]}` : names.join(" and ");
  return `${names.slice(0, 2).join(", ")} and ${names.length - 2} more`;
}

/**
 * All the steps of a run, oldest first. A step whose tool has answered loses its trailing ellipsis and gains what came of
 * it ("12 of 13 pass"); the one the editor is still waiting on is `pending`. Clips it looked at one after the other are one step.
 */
export function runSteps(events: readonly RunEvent[], titles: ReadonlyMap<string, string>, { live = true }: { live?: boolean } = {}): RunStep[] {
  const steps: RunStep[] = [];
  /** The titles behind the last "looking at clips" step, to keep adding to it. */
  let looked: string[] | null = null;
  events.forEach((event, index) => {
    const step = describeEvent(event, titles);
    if (!step) return;
    if (event.type === "tool_call") {
      const answer = answerTo(events, index);
      const detail = answer ? stepDetail(event.tool, answer.text) : undefined;
      step.label = answer ? withoutEllipsis(step.label) : step.label;
      if (detail) step.detail = detail;
      // Only a run that is working has a call in flight; a run that was stopped, failed or waits for the creator has none.
      // A tool that failed has no `answer` here; its failure is its own step right after, so the call is not left spinning.
      if (live && !answer && !hasLaterResult(events, index, event.tool)) step.pending = true;
    }
    if (step.kind === "view_clip") {
      const id = ASSET_ID.exec(event.text)?.[0];
      const title = (id ? titles.get(id) : undefined) ?? "a clip";
      const last = steps.at(-1);
      if (last?.kind === "view_clip" && looked) {
        looked.push(title);
        last.label = `Looking at ${nameList(looked)}${step.pending ? "…" : ""}`;
        last.pending = step.pending;
        return;
      }
      looked = [title];
    } else {
      looked = null;
    }
    steps.push(step);
  });
  return steps;
}

/**
 * What the run is doing now, in a sentence. The newest tool call is in flight when no result has come after it; between
 * calls the model is thinking.
 */
export function currentActivity(run: Pick<Run, "status" | "events" | "question">, titles: ReadonlyMap<string, string>): string {
  switch (run.status) {
    case "queued":
      return "Waiting for the editor to start…";
    case "waiting_input":
      return "Waiting for your answer";
    case "succeeded":
      return "Done";
    case "failed":
      return "Stopped";
    case "cancelled":
      return "Stopped by you";
    default: {
      const last = [...run.events].reverse().find((event) => event.type === "tool_call" || event.type === "tool_result" || event.type === "assistant_message");
      if (!last) return "Getting started…";
      return last.type === "tool_call" ? toolLabel(last.tool, last.text, titles) : "Deciding the next step…";
    }
  }
}

const FAILURES: Readonly<Record<string, string>> = {
  budget: "The editor reached its limit before it finished.",
  daily_cap: "The daily spending limit for AI editing is reached. It resets tomorrow.",
  request_cap: "Too many AI requests right now. Try again in a minute.",
  model_error: "The AI model had a problem. Try again.",
  model_access: "AI editing isn't available right now.",
  model_outcome_unknown: "The editor lost its connection to the AI model. Try again.",
  refusal: "The AI model declined to work on this. Try other instructions.",
  too_many_resumes: "The editor kept being interrupted. Try again.",
  context_window_exceeded: "There was too much to keep track of. Try again with fewer clips.",
  internal_error: "Something went wrong on our side. Try again.",
};

const LIMITS: Readonly<Record<string, string>> = {
  dollars: "its spending cap",
  tokens: "its size limit",
  tool_calls: "its step limit",
  wall_clock: "its time limit",
};

/** Why a run failed, in words for the creator. An unknown reason gets the general line. */
export function failureMessage(reason: string | null | undefined): string {
  return (reason ? FAILURES[reason] : undefined) ?? "The editor stopped before it finished. Try again.";
}

/** Which limit a budget failure hit, found in the run's status events (`failed reason=budget limit=dollars`). */
export function budgetLimit(events: readonly RunEvent[]): string | undefined {
  for (const event of [...events].reverse()) {
    const limit = event.type === "status" ? /\blimit=(\w+)/.exec(event.text)?.[1] : undefined;
    if (limit) return LIMITS[limit];
  }
  return undefined;
}

/** "$0.42"; amounts under a cent keep a third decimal ("$0.004"). The API counts millionths of a dollar. */
export function formatUsd(micros: number): string {
  const dollars = micros / 1_000_000;
  return dollars > 0 && dollars < 0.01 ? `$${dollars.toFixed(3)}` : `$${dollars.toFixed(2)}`;
}

/** "42s", "3m 05s". */
export function formatElapsed(ms: number): string {
  const total = Math.max(0, Math.floor(ms / 1000));
  const minutes = Math.floor(total / 60);
  return minutes === 0 ? `${total}s` : `${minutes}m ${String(total % 60).padStart(2, "0")}s`;
}
