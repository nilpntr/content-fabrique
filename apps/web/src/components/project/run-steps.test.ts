import { expect, test } from "vitest";
import { assetId, event, run } from "@/test-fixtures";
import { budgetLimit, currentActivity, describeEvent, failureMessage, formatElapsed, formatUsd, runSteps, toolLabel } from "./run-steps";

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
  expect(describeEvent(event(5, "error", "model_error: overloaded", { is_error: true }), titles)).toEqual({ seq: 5, label: "model_error: overloaded", tone: "error" });
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
