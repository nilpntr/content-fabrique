import { expect, test } from "vitest";
import { mainItems } from "@/components/project/review-data";
import { assetId, CHECKS_ROUND_1, CHECKS_ROUND_2, event, v2Run, v2Spec } from "@/test-fixtures";
import { canRetry, EDIT_STAGES, editProgress, furthestStageOf, humanizeChange, reviewSummary, stageState, stoppedMessage } from "./plain-words";
import { checkRoundFromArtifact } from "./run-artifacts";

const toolCall = (seq: number, tool: string) => event(seq, "tool_call", `${tool} {}`, { tool });

test("while editing, the screen names one of four calm stages, never a tool", () => {
  expect(EDIT_STAGES.map((stage) => stage.label)).toEqual(["Watching your clips", "Picking the best reps", "Cutting to the music", "Checking the result"]);
  expect(editProgress({ status: "queued", events: [] })).toEqual({ stage: -1, headline: "Getting ready…", polishing: false });
  expect(editProgress({ status: "running", events: [] }).headline).toBe("Getting ready…");
  expect(editProgress({ status: "running", events: [toolCall(1, "get_brief"), toolCall(2, "view_clip")] })).toEqual({ stage: 0, headline: "Watching your clips…", polishing: false });
  expect(editProgress({ status: "running", events: [toolCall(1, "view_clip"), toolCall(2, "plan_edit")] }).headline).toBe("Picking the best reps…");
  expect(editProgress({ status: "running", events: [toolCall(1, "plan_edit"), toolCall(2, "edit_timeline")] }).headline).toBe("Cutting to the music…");
  expect(editProgress({ status: "running", events: [toolCall(1, "edit_timeline"), toolCall(2, "check_edit")] }).headline).toBe("Checking the result…");
  expect(editProgress({ status: "running", events: [toolCall(1, "check_edit"), toolCall(2, "finish")] }).headline).toBe("Almost done…");
  expect(editProgress({ status: "waiting_input", events: [toolCall(1, "view_clip")] }).headline).toBe("I have a question for you");
  // A tool this build does not know does not move the stage.
  expect(editProgress({ status: "running", events: [toolCall(1, "plan_edit"), toolCall(2, "a_new_tool")] }).stage).toBe(1);
});

test("after checking, going back to fix something says so; the stages before stay done and the check comes again", () => {
  const run = { status: "running" as const, events: [toolCall(1, "view_clip"), toolCall(2, "plan_edit"), toolCall(3, "edit_timeline"), toolCall(4, "check_edit"), toolCall(5, "match_exposure")] };
  const progress = editProgress(run);
  expect(progress).toEqual({ stage: 2, headline: "Fixing a few things I noticed…", polishing: true });
  const furthest = furthestStageOf(run);
  expect(EDIT_STAGES.map((_, index) => stageState(index, progress, furthest))).toEqual(["done", "done", "current", "todo"]);
  const early = editProgress({ status: "running", events: [toolCall(1, "plan_edit")] });
  expect(EDIT_STAGES.map((_, index) => stageState(index, early, 1))).toEqual(["done", "current", "todo", "todo"]);
});

test("the self-review is one friendly line, with the checks in plain names one click away", () => {
  const first = checkRoundFromArtifact(CHECKS_ROUND_1, 1);
  const second = checkRoundFromArtifact(CHECKS_ROUND_2, 2);
  if (!first || !second) throw new Error("no rounds");
  expect(reviewSummary([])).toBeUndefined();
  const fixed = reviewSummary([first, second]);
  expect(fixed?.line).toBe("I fixed 1 thing before showing you this");
  expect(fixed?.tone).toBe("fixed");
  expect(fixed?.items).toHaveLength(13);
  expect(fixed?.items.find((item) => item.state === "fixed")?.name).toBe("Even brightness");
  // No ids or codes in what the creator reads.
  expect(fixed?.items.map((item) => item.name).join(" ")).not.toMatch(/_/);

  expect(reviewSummary([second])?.line).toBe("I checked 13 things — all good");
  const notYet = reviewSummary([first]);
  expect(notYet?.line).toBe("I checked 13 things. 1 could still be better.");
  expect(notYet?.tone).toBe("not_yet");
});

test("why an edit stopped is a plain sentence, and some reasons are not worth a retry", () => {
  expect(stoppedMessage("budget")).toBe("This edit was bigger than I could finish in one go.");
  expect(stoppedMessage("something_new")).toBe("I stopped before I could finish.");
  expect(stoppedMessage(null)).toBe("I stopped before I could finish.");
  expect(canRetry("budget")).toBe(true);
  expect(canRetry("refusal")).toBe(false);
  expect(canRetry("daily_cap")).toBe(false);
});

test("a version's change lines name clips, not timeline ids", () => {
  const items = mainItems(v2Spec());
  const names = { [assetId(1)]: "Heaviest lat pulldown", [assetId(2)]: "Seated cable row" };
  expect(humanizeChange("Evened out the brightness of c4 and c5", items, names)).toBe("Evened out the brightness of Heaviest lat pulldown and Seated cable row");
  expect(humanizeChange("Moved c9 earlier", items, names)).toBe("Moved c9 earlier");
  expect(v2Run().summary).toBeTruthy();
});
