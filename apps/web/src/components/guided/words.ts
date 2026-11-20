/**
 * The words and the small decisions of the guided "Make a video" flow (docs/design/guided-flow.md): the three questions and their
 * answers, the sentences they become for the editor, the steps of the progress screen and the quick changes of the result screen.
 * Everything a creator reads in the flow is here or in the component that shows it; none of it names a tool, a version or a code.
 */
import type { EditVibe, Run, RunEvent } from "@/lib/api/client";

// ---- The questions -------------------------------------------------------------------------------------------------------------

export const KINDS = [
  { id: "workout", label: "Workout", hint: "Training, exercises, gym days", brief: "a workout video" },
  { id: "talking", label: "Talking", hint: "You, speaking to the camera", brief: "a video of me talking to the camera" },
  { id: "product", label: "Product", hint: "Showing something off", brief: "a product video" },
  { id: "vlog", label: "Vlog", hint: "A day, a trip, behind the scenes", brief: "a vlog" },
] as const;
export type KindId = (typeof KINDS)[number]["id"];

export const LENGTHS = [
  { id: "short", label: "Short", hint: "About 15 seconds", seconds: 15 },
  { id: "medium", label: "Medium", hint: "About 30 seconds", seconds: 30 },
  { id: "long", label: "Long", hint: "About a minute", seconds: 60 },
  { id: "auto", label: "You decide", hint: "Whatever fits the clips", seconds: null },
] as const;
export type LengthId = (typeof LENGTHS)[number]["id"];

export const STYLES = [
  { id: "mine", label: "My usual style", hint: "", vibe: null },
  { id: "calm", label: "Calm", hint: "Slow and steady", vibe: "calm" },
  { id: "lively", label: "Lively", hint: "Moves with the music", vibe: "lively" },
  { id: "hype", label: "Hype", hint: "Fast and punchy", vibe: "hype" },
] as const satisfies readonly { id: string; label: string; hint: string; vibe: EditVibe | null }[];
export type StyleId = (typeof STYLES)[number]["id"];

/** The line under "My usual style": whether there is a learnt style to follow. */
export const usualStyleHint = (learnt: boolean): string => (learnt ? "Like the videos you showed me" : "I'll pick what fits your clips");

export interface Answers {
  kind: KindId;
  length: LengthId;
  style: StyleId;
}

export const kindOf = (id: KindId) => KINDS.find((kind) => kind.id === id) as (typeof KINDS)[number];
export const lengthOf = (id: LengthId) => LENGTHS.find((length) => length.id === id) as (typeof LENGTHS)[number];
export const styleOf = (id: StyleId) => STYLES.find((style) => style.id === id) as (typeof STYLES)[number];

/**
 * The answers as sentences for the editor. The API has no fields for the kind of video, its length or which file is the song, so
 * they travel in the instructions (the follow-ups in docs/design/guided-flow.md).
 */
export function buildBrief(answers: Pick<Answers, "kind" | "length">, songName?: string): string {
  const lines = [`Make ${kindOf(answers.kind).brief}.`];
  const seconds = lengthOf(answers.length).seconds;
  if (seconds !== null) lines.push(`Keep it about ${seconds} seconds long.`);
  if (songName) lines.push(`Use the song I added (${songName}) as the music.`);
  return lines.join(" ");
}

/** The vibe to ask for, or undefined to follow the creator's own style. */
export const vibeOf = (style: StyleId): EditVibe | undefined => styleOf(style).vibe ?? undefined;

/** "Workout video · 20 Nov": how the video is listed under Projects. */
export function videoTitle(kind: KindId, now: Date = new Date()): string {
  const day = now.toLocaleDateString("en-GB", { day: "numeric", month: "short" });
  return `${kindOf(kind).label} video · ${day}`;
}

/** "3 clips and a song". */
export function clipCountWords(clips: number, song: boolean): string {
  const count = `${clips} ${clips === 1 ? "clip" : "clips"}`;
  return song ? `${count} and a song` : count;
}

// ---- The progress screen -------------------------------------------------------------------------------------------------------

/** The steps of making a video, as the creator sees them, in order. */
export const MAKING_STEPS = [
  { id: "watch", label: "Watching your clips" },
  { id: "moments", label: "Finding your best moments" },
  { id: "cut", label: "Cutting to the beat" },
  { id: "captions", label: "Adding captions" },
  { id: "check", label: "Checking it over" },
] as const;
export type MakingStepId = (typeof MAKING_STEPS)[number]["id"];

const STEP_OF_TOOL: Readonly<Record<string, MakingStepId>> = {
  get_brief: "watch",
  get_comments: "watch",
  list_footage: "watch",
  view_clip: "watch",
  find_moments: "moments",
  plan_edit: "moments",
  get_timeline: "cut",
  edit_timeline: "cut",
  match_exposure: "cut",
  render_preview: "check",
  check_edit: "check",
  finish: "check",
};

/** An edit that writes words on screen: the event's one-line summary names what the edit does. */
const WRITES_WORDS = /"(add_text|update_text|set_captions)"/;

const indexOfStep = (id: MakingStepId): number => MAKING_STEPS.findIndex((step) => step.id === id);

/** The step each of the editor's actions belongs to, oldest first. An action this build does not know is left out. */
function stepsOf(events: readonly RunEvent[]): number[] {
  return events.flatMap((event) => {
    if (event.type !== "tool_call" || !event.tool) return [];
    const step = event.tool === "edit_timeline" && WRITES_WORDS.test(event.text) ? "captions" : STEP_OF_TOOL[event.tool];
    return step ? [indexOfStep(step)] : [];
  });
}

export type StepState = "done" | "now" | "todo";

export interface MakingProgress {
  /** One calm line about now. */
  headline: string;
  /** The state of each of {@link MAKING_STEPS}, in order. */
  states: StepState[];
}

const CHECK = indexOfStep("check");

/**
 * Where the editor is, from its events: the step of its newest action is "now", and every other step up to the furthest one it
 * reached is done (so a step it had no need for is ticked when a later one starts, and going back to an earlier one unticks
 * nothing). Going back after it has checked its work is "fixing"; cutting some more after the first captions is just cutting.
 *
 * "Adding captions" is read from the one-line summary of an edit, which is cut short: captions written at the end of a large edit
 * are not seen, and the step is then ticked when the checking starts.
 */
export function makingProgress(run: Pick<Run, "status" | "events">): MakingProgress {
  const steps = stepsOf(run.events ?? []);
  const current = run.status === "queued" ? -1 : (steps.at(-1) ?? -1);
  const furthest = Math.max(current, ...steps);
  const states = MAKING_STEPS.map((_, index): StepState => (index === current ? "now" : index <= furthest ? "done" : "todo"));
  const last = [...(run.events ?? [])].reverse().find((event) => event.type === "tool_call");
  let headline: string;
  if (current < 0) headline = "Getting ready…";
  else if (last?.tool === "finish") headline = "Almost done…";
  else if (current < furthest && furthest === CHECK) headline = "Fixing a few things I noticed…";
  else headline = `${MAKING_STEPS[current]?.label ?? "Working"}…`;
  return { headline, states };
}

// ---- The result screen ---------------------------------------------------------------------------------------------------------

/** The quick changes: a tap adds the sentence to what the editor is asked. "More energy" also asks for the hype vibe. */
export const CHANGE_CHIPS = [
  { id: "shorter", label: "Shorter", ask: "Make it shorter.", vibe: null },
  { id: "longer", label: "Longer", ask: "Make it longer.", vibe: null },
  { id: "energy", label: "More energy", ask: "Give it more energy, with faster cuts.", vibe: "hype" },
  { id: "music", label: "Different music", ask: "Use different music.", vibe: null },
] as const satisfies readonly { id: string; label: string; ask: string; vibe: EditVibe | null }[];
export type ChangeChipId = (typeof CHANGE_CHIPS)[number]["id"];

/** The longest instruction the API takes. */
export const CHANGE_MAX_LENGTH = 2000;

/** What "Make the changes" sends: the picked chips' sentences, then the creator's own words. Undefined when there is nothing to ask. */
export function buildChange(picked: ReadonlySet<ChangeChipId>, text: string): { instructions: string; vibe?: EditVibe } | undefined {
  const chips = CHANGE_CHIPS.filter((chip) => picked.has(chip.id));
  const instructions = [...chips.map((chip) => chip.ask), text.trim()].filter((part) => part !== "").join(" ").slice(0, CHANGE_MAX_LENGTH);
  if (instructions === "") return undefined;
  const vibe = chips.find((chip) => chip.vibe !== null)?.vibe ?? undefined;
  return { instructions, ...(vibe ? { vibe } : {}) };
}
