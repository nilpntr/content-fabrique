import { expect, test } from "vitest";
import { event, run } from "@/test-fixtures";
import { buildBrief, buildChange, CHANGE_CHIPS, clipCountWords, KINDS, LENGTHS, MAKING_STEPS, makingProgress, STYLES, usualStyleHint, vibeOf, videoTitle } from "./words";

const call = (seq: number, tool: string, text = tool) => event(seq, "tool_call", text, { tool });
const labelsIn = (states: string[], state: string) => MAKING_STEPS.filter((_, index) => states[index] === state).map((step) => step.label);

test("the questions offer what the brief asked for, in the creator's words", () => {
  expect(KINDS.map((kind) => kind.label)).toEqual(["Workout", "Talking", "Product", "Vlog"]);
  expect(LENGTHS.map((length) => length.label)).toEqual(["Short", "Medium", "Long", "You decide"]);
  expect(STYLES.map((style) => style.label)).toEqual(["My usual style", "Calm", "Lively", "Hype"]);
});

test("no word of the flow is jargon", () => {
  const words: string[] = [...[...KINDS, ...LENGTHS, ...STYLES].flatMap((option) => [option.label, option.hint]), ...MAKING_STEPS.map((step) => step.label), ...CHANGE_CHIPS.map((chip) => chip.label)];
  for (const word of words) expect(word).not.toMatch(/timeline|asset|render|\bops?\b|\brun\b|version|credit|token/i);
});

test("the answers become sentences for the editor; 'You decide' and no song say nothing", () => {
  expect(buildBrief({ kind: "workout", length: "medium" }, "beat.mp3")).toBe("Make a workout video. Keep it about 30 seconds long. Use the song I added (beat.mp3) as the music.");
  expect(buildBrief({ kind: "vlog", length: "auto" })).toBe("Make a vlog.");
});

test("'My usual style' asks for no vibe, the others for theirs", () => {
  expect(vibeOf("mine")).toBeUndefined();
  expect(vibeOf("calm")).toBe("calm");
  expect(vibeOf("hype")).toBe("hype");
  expect(usualStyleHint(true)).toBe("Like the videos you showed me");
  expect(usualStyleHint(false)).toBe("I'll pick what fits your clips");
});

test("a video is listed by its kind and the day", () => {
  expect(videoTitle("workout", new Date(2026, 10, 20))).toBe("Workout video · 20 Nov");
});

test("clips are counted in words", () => {
  expect(clipCountWords(1, false)).toBe("1 clip");
  expect(clipCountWords(3, true)).toBe("3 clips and a song");
});

test("before the editor starts, nothing is ticked and it is getting ready", () => {
  const progress = makingProgress(run({ status: "queued" }));
  expect(progress.headline).toBe("Getting ready…");
  expect(progress.states).toEqual(["todo", "todo", "todo", "todo", "todo"]);
});

test("the newest action decides the step in progress, and the steps before it are done", () => {
  const progress = makingProgress(run({ status: "running", events: [call(1, "list_footage"), call(2, "find_moments"), call(3, "edit_timeline", 'edit_timeline {"base_version":0,"ops":[{"op":"add_clip"')] }));
  expect(progress.headline).toBe("Cutting to the beat…");
  expect(labelsIn(progress.states, "done")).toEqual(["Watching your clips", "Finding your best moments"]);
  expect(labelsIn(progress.states, "now")).toEqual(["Cutting to the beat"]);
});

test("an edit that writes words on screen is 'Adding captions'", () => {
  const progress = makingProgress(run({ status: "running", events: [call(1, "edit_timeline", 'edit_timeline {"base_version":1,"ops":[{"op":"add_text","text":"BACK DAY"')] }));
  expect(progress.headline).toBe("Adding captions…");
  expect(labelsIn(progress.states, "now")).toEqual(["Adding captions"]);
});

test("a step the editor skipped is ticked when a later one starts", () => {
  const progress = makingProgress(run({ status: "running", events: [call(1, "list_footage"), call(2, "check_edit")] }));
  expect(labelsIn(progress.states, "done")).toEqual(["Watching your clips", "Finding your best moments", "Cutting to the beat", "Adding captions"]);
  expect(labelsIn(progress.states, "now")).toEqual(["Checking it over"]);
});

test("going back after checking is fixing, and finishing is almost done", () => {
  const fixing = makingProgress(run({ status: "running", events: [call(1, "check_edit"), call(2, "edit_timeline")] }));
  expect(fixing.headline).toBe("Fixing a few things I noticed…");
  expect(labelsIn(fixing.states, "now")).toEqual(["Cutting to the beat"]);
  expect(makingProgress(run({ status: "running", events: [call(1, "check_edit"), call(2, "finish")] })).headline).toBe("Almost done…");
});

test("cutting some more after the first captions is cutting, not fixing, and the captions stay ticked", () => {
  const text = 'edit_timeline {"base_version":1,"ops":[{"op":"add_text"';
  const progress = makingProgress(run({ status: "running", events: [call(1, "edit_timeline"), call(2, "edit_timeline", text), call(3, "edit_timeline")] }));
  expect(progress.headline).toBe("Cutting to the beat…");
  expect(labelsIn(progress.states, "done")).toEqual(["Watching your clips", "Finding your best moments", "Adding captions"]);
});

test("an action this build does not know keeps the step", () => {
  const progress = makingProgress(run({ status: "running", events: [call(1, "find_moments"), call(2, "brand_new_tool")] }));
  expect(labelsIn(progress.states, "now")).toEqual(["Finding your best moments"]);
});

test("a change is the picked chips' sentences and the creator's words; nothing picked is nothing to ask", () => {
  expect(buildChange(new Set(), "  ")).toBeUndefined();
  expect(buildChange(new Set(["shorter"]), "")).toEqual({ instructions: "Make it shorter." });
  expect(buildChange(new Set(["music", "shorter"]), " Bigger title ")).toEqual({ instructions: "Make it shorter. Use different music. Bigger title" });
});

test("'More energy' also asks for the hype vibe", () => {
  expect(buildChange(new Set(["energy"]), "")).toEqual({ instructions: "Give it more energy, with faster cuts.", vibe: "hype" });
});
