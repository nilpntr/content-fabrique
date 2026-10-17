import { expect, test } from "vitest";
import { assetId, event, run, v2RunEvents } from "@/test-fixtures";
import { budgetLimit, currentActivity, describeEvent, failureMessage, formatElapsed, formatUsd, runSteps, stepDetail, stepKind, toolLabel } from "./run-steps";

test("the tools of editor v2 have words and a kind for their icon", () => {
  expect(toolLabel("find_moments", 'find_moments {"query":"x"}', titles)).toBe("Finding the best moments");
  expect(toolLabel("plan_edit", "plan_edit {", titles)).toBe("Planning the edit…");
  expect(toolLabel("match_exposure", "match_exposure {}", titles)).toBe("Evening out the brightness…");
  expect(toolLabel("check_edit", "check_edit {}", titles)).toBe("Checking its own work…");
  const kinds = ["find_moments", "view_clip", "plan_edit", "edit_timeline", "render_preview", "check_edit", "match_exposure", "finish"].map(stepKind);
  expect(kinds).toEqual(["find_moments", "view_clip", "plan", "edit", "preview", "check", "match_exposure", "finish"]);
  expect(stepKind("a_new_tool")).toBe("working");
  expect(stepKind(null)).toBe("working");
});

test("the v2 run reads as a short list of steps, each with its kind, what came of it, and clips looked at in a row as one", () => {
  const steps = runSteps(v2RunEvents(), new Map([[assetId(1), "Lat pulldown"], [assetId(2), "Seated row"], [assetId(3), "Posing"]]));
  expect(steps.map((step) => [step.kind, step.label, step.detail])).toEqual([
    ["brief", "Reading your instructions", undefined],
    ["footage", "Looking through your footage", undefined],
    ["find_moments", "Finding the best moments", undefined],
    ["view_clip", "Looking at Lat pulldown, Seated row and Posing", undefined],
    ["plan", "Planning the edit", "workout · 124 BPM · 6 shots"],
    ["edit", "Editing the timeline (v1)", undefined],
    ["preview", "Rendering a preview", undefined],
    ["check", "Checking its own work", "12 of 13 pass, 1 to fix"],
    ["match_exposure", "Evening out the brightness", "2 clips evened out"],
    ["preview", "Rendering a preview", undefined],
    ["check", "Checking its own work", "13 of 13 pass"],
    ["finish", "Writing up the result", undefined],
  ]);
  // Every call was answered: nothing spins.
  expect(steps.some((step) => step.pending)).toBe(false);
});

test("the call the editor is waiting on is pending, and keeps its ellipsis", () => {
  const steps = runSteps([event(1, "tool_call", "render_preview {}", { tool: "render_preview" })], titles);
  expect(steps).toEqual([{ seq: 1, kind: "preview", tone: "normal", label: "Rendering a preview…", pending: true }]);
  // A call that failed is not pending: the failure is its own step.
  const failed = runSteps([event(1, "tool_call", "edit_timeline {}", { tool: "edit_timeline" }), event(2, "tool_result", "bad", { tool: "edit_timeline", is_error: true })], titles);
  expect(failed.map((step) => [step.kind, step.pending])).toEqual([
    ["edit", undefined],
    ["error", undefined],
  ]);
});

test("a run that is not working has no call in flight, and a group of looks keeps the key of its first look", () => {
  const call = [event(1, "tool_call", "render_preview {}", { tool: "render_preview" })];
  expect(runSteps(call, titles, { live: false })[0]?.pending).toBeUndefined();
  expect(runSteps(call, titles, { live: true })[0]?.pending).toBe(true);
  const looks = [event(5, "tool_call", `view_clip {"asset_id":"${assetId(2)}"}`, { tool: "view_clip" }), event(7, "tool_call", `view_clip {"asset_id":"${assetId(2)}"}`, { tool: "view_clip" })];
  expect(runSteps(looks, titles).map((step) => step.seq)).toEqual([5]);
});

test("clips looked at in a row are named up to three, then counted", () => {
  const views = (n: number) => Array.from({ length: n }, (_, i) => event(i + 1, "tool_call", `view_clip {"asset_id":"${assetId(i + 1)}"}`, { tool: "view_clip" }));
  const names = new Map(Array.from({ length: 6 }, (_, i): [string, string] => [assetId(i + 1), `Clip ${i + 1}`]));
  expect(runSteps(views(2), names).map((step) => step.label)).toEqual(["Looking at Clip 1 and Clip 2…"]);
  expect(runSteps(views(5), names).map((step) => step.label)).toEqual(["Looking at Clip 1, Clip 2 and 3 more…"]);
  // A step between two looks keeps them apart.
  const split = [...views(1), event(5, "tool_call", "get_timeline {}", { tool: "get_timeline" }), event(6, "tool_call", `view_clip {"asset_id":"${assetId(2)}"}`, { tool: "view_clip" })];
  expect(runSteps(split, names)).toHaveLength(3);
});

test("a limit that was reached and an answer that was cut off are notes in the feed", () => {
  const steps = runSteps(
    [
      event(1, "status", "cut_off"),
      event(2, "status", "limit_reached limit=tool_calls"),
      event(3, "status", "succeeded limit=wall_clock"),
      event(4, "status", "running"),
      event(5, "status", "cost_reconciled"),
    ],
    titles,
  );
  expect(steps.map((step) => [step.tone, step.label])).toEqual([
    ["note", "The editor's answer was cut off, so it tried again"],
    ["note", "The editor reached its step limit and is wrapping up"],
    ["note", "Finished early, at its time limit"],
  ]);
});

test("what came of a step is told in a few words", () => {
  expect(stepDetail("match_exposure", "Nothing to change: the 3 measured clips are within 0.15 stops")).toBe("already even");
  expect(stepDetail("match_exposure", "Matched the brightness of 1 clip toward p90 luma 0.20")).toBe("1 clip evened out");
  expect(stepDetail("check_edit", "garbled")).toBeUndefined();
  expect(stepDetail("plan_edit", "garbled")).toBeUndefined();
  expect(stepDetail("get_brief", "anything")).toBeUndefined();
});

const titles = new Map([[assetId(2), "Row 2"]]);

test("a tool call reads as a plain sentence, with the clip's title and the version being made", () => {
  expect(toolLabel("get_brief", "get_brief {}", titles)).toBe("Reading your instructions");
  expect(toolLabel("list_footage", "list_footage {}", titles)).toBe("Looking through your footage");
  expect(toolLabel("view_clip", `view_clip {"asset_id":"${assetId(2)}"}`, titles)).toBe("Looking at Row 2…");
  expect(toolLabel("view_clip", `view_clip {"asset_id":"${assetId(9)}"}`, titles)).toBe("Looking at a clip…");
  expect(toolLabel("get_timeline", "get_timeline {}", titles)).toBe("Checking the timeline…");
  expect(toolLabel("edit_timeline", 'edit_timeline {"base_version":2,"ops":[{"op":"add_clip"', titles)).toBe("Editing the timeline (v3)…");
  expect(toolLabel("edit_timeline", "edit_timeline {", titles)).toBe("Editing the timeline…");
  expect(toolLabel("render_preview", "render_preview {}", titles)).toBe("Rendering a preview…");
  expect(toolLabel("finish", "finish {...", titles)).toBe("Writing up the result…");
  expect(toolLabel("something_new", "", titles)).toBe("Working…");
});

test("only tool calls and failures are steps; statuses, messages and good results are not", () => {
  expect(describeEvent(event(1, "status", "running"), titles)).toBeNull();
  expect(describeEvent(event(2, "assistant_message", "Let me look"), titles)).toBeNull();
  expect(describeEvent(event(3, "tool_result", "ok", { tool: "view_clip" }), titles)).toBeNull();
  expect(describeEvent(event(4, "tool_result", "bad", { tool: "edit_timeline", is_error: true }), titles)).toMatchObject({ tone: "error" });
  expect(describeEvent(event(5, "error", "model_error: overloaded", { is_error: true }), titles)).toEqual({ seq: 5, label: "model_error: overloaded", tone: "error", kind: "error" });
  const events = [event(1, "status", "running"), event(2, "tool_call", "list_footage {}", { tool: "list_footage" }), event(3, "tool_result", "x", { tool: "list_footage" })];
  expect(runSteps(events, titles).map((step) => step.label)).toEqual(["Looking through your footage"]);
});

test("what the run is doing now follows its status and its newest event", () => {
  const call = event(2, "tool_call", `view_clip {"asset_id":"${assetId(2)}"}`, { tool: "view_clip" });
  expect(currentActivity(run({ status: "queued" }), titles)).toBe("Waiting for the editor to start…");
  expect(currentActivity(run({ status: "running" }), titles)).toBe("Getting started…");
  expect(currentActivity(run({ status: "running", events: [event(1, "status", "running"), call] }), titles)).toBe("Looking at Row 2…");
  // A result came back after the call: the model is choosing what to do next.
  expect(currentActivity(run({ status: "running", events: [call, event(3, "tool_result", "ok", { tool: "view_clip" })] }), titles)).toBe("Deciding the next step…");
  expect(currentActivity(run({ status: "waiting_input" }), titles)).toBe("Waiting for your answer");
  expect(currentActivity(run({ status: "succeeded" }), titles)).toBe("Done");
  expect(currentActivity(run({ status: "cancelled" }), titles)).toBe("Stopped by you");
});

test("a failure is explained, a budget failure says which limit, and an unknown reason gets the general line", () => {
  expect(failureMessage("daily_cap")).toMatch(/daily spending limit/);
  expect(failureMessage("model_error")).toMatch(/Try again/);
  expect(failureMessage("brand_new_reason")).toBe("The editor stopped before it finished. Try again.");
  expect(failureMessage(null)).toBe("The editor stopped before it finished. Try again.");
  expect(budgetLimit([event(1, "status", "running"), event(9, "status", "failed reason=budget limit=dollars")])).toBe("its spending cap");
  expect(budgetLimit([event(1, "status", "running")])).toBeUndefined();
});

test("cost and time are written for people", () => {
  expect(formatUsd(0)).toBe("$0.00");
  expect(formatUsd(420_000)).toBe("$0.42");
  expect(formatUsd(4_000)).toBe("$0.004");
  expect(formatUsd(5_000_000)).toBe("$5.00");
  expect(formatElapsed(0)).toBe("0s");
  expect(formatElapsed(42_900)).toBe("42s");
  expect(formatElapsed(185_000)).toBe("3m 05s");
});
