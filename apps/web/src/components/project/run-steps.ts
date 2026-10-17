import type { Run, RunEvent } from "@/lib/api/client";

/** A line of the run feed in the creator's words. */
export interface RunStep {
  seq: number;
  label: string;
  /** `error` is a step that failed. */
  tone: "normal" | "error";
}

const ASSET_ID = /ast_[0-9a-z]{26}/;

/** The `base_version` of an edit_timeline call, from the one-line summary `edit_timeline {"base_version":2,"ops":[...` (cut at 400 bytes, so it is read with a pattern and not parsed). */
function baseVersionOf(text: string): number | undefined {
  const match = /"base_version"\s*:\s*(\d+)/.exec(text);
  return match ? Number(match[1]) : undefined;
}

/** What a tool call means for the creator. `titles` maps an asset id to the clip's title. */
export function toolLabel(tool: string | null | undefined, text: string, titles: ReadonlyMap<string, string>): string {
  switch (tool) {
    case "get_brief":
      return "Reading your instructions";
    case "list_footage":
      return "Looking through your footage";
    case "view_clip": {
      const id = ASSET_ID.exec(text)?.[0];
      const title = id ? titles.get(id) : undefined;
      return title ? `Looking at ${title}…` : "Looking at a clip…";
    }
    case "get_timeline":
      return "Checking the timeline…";
    case "edit_timeline": {
      const base = baseVersionOf(text);
      return base === undefined ? "Editing the timeline…" : `Editing the timeline (v${base + 1})…`;
    }
    case "render_preview":
      return "Rendering a preview…";
    case "finish":
      return "Writing up the result…";
    case "ask_user":
      return "Asking you a question…";
    default:
      return "Working…";
  }
}

/** The step an event stands for, or null for the events the feed does not show (statuses, the model's own messages, results that went well). */
export function describeEvent(event: RunEvent, titles: ReadonlyMap<string, string>): RunStep | null {
  if (event.type === "tool_call") return { seq: event.seq, label: toolLabel(event.tool, event.text, titles), tone: "normal" };
  if (event.type === "tool_result" && event.is_error) return { seq: event.seq, label: "A step did not go through; the editor is adjusting", tone: "error" };
  if (event.type === "error") return { seq: event.seq, label: event.text || "Something went wrong", tone: "error" };
  return null;
}

/** All the steps of a run, oldest first. */
export function runSteps(events: readonly RunEvent[], titles: ReadonlyMap<string, string>): RunStep[] {
  return events.flatMap((event) => describeEvent(event, titles) ?? []);
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
