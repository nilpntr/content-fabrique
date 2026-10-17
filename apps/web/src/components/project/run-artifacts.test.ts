import { expect, test } from "vitest";
import { CHECKS_ROUND_1, CHECKS_ROUND_2, checkText, event, PLAN_PAYLOAD, summarize, v2RunEvents } from "@/test-fixtures";
import { applicable, checkRoundFromArtifact, checkRoundFromSummary, matchFromSummary, planFromArtifact, planFromSummaries, runArtifacts } from "./run-artifacts";

const events = v2RunEvents();
const text = (seq: number) => events.find((e) => e.seq === seq)?.text ?? "";

test("the documented creative_plan payload is read in full", () => {
  const plan = planFromArtifact(PLAN_PAYLOAD);
  expect(plan).toMatchObject({
    contentType: "workout",
    playbook: "workout",
    story: "Gino's back day in 25 seconds: three pulls, one posing finish.",
    targetLengthS: 25,
    tempoBpm: 124,
    pace: "building",
    estimatedLengthMs: 24_200,
    shotCount: 6,
  });
  expect(plan?.shots).toHaveLength(6);
  // `none` is no effect; the others are kept.
  expect(plan?.shots[1]).toEqual({ role: "build", what: "Seated row, full rep", assetId: expect.any(String), beats: 4, lengthMs: 1936 });
  expect(plan?.shots[0]?.effect).toBe("punch_in");
});

test("a plan with no beat grid has a null tempo, and a payload of another version or shape is not read", () => {
  expect(planFromArtifact({ ...PLAN_PAYLOAD, tempo_bpm: 0 })?.tempoBpm).toBeNull();
  expect(planFromArtifact({ ...PLAN_PAYLOAD, version: 2 })).toBeUndefined();
  expect(planFromArtifact({ version: 1 })).toBeUndefined();
  expect(planFromArtifact("nope")).toBeUndefined();
  expect(planFromArtifact({ ...PLAN_PAYLOAD, shots: [null, { role: "hook" }, { role: "hook", beats: 2 }] })?.shots).toEqual([{ role: "hook", beats: 2 }]);
});

test("the documented edit_check payload is read, and a pass that says borderline is borderline", () => {
  const round = checkRoundFromArtifact(CHECKS_ROUND_1, 1);
  expect(round).toMatchObject({ nth: 1, version: 1, passed: 12, failed: 1, skipped: 0 });
  expect(applicable(round as NonNullable<typeof round>)).toBe(13);
  expect(round?.checks).toHaveLength(13);
  expect(round?.checks.find((check) => check.id === "brightness_jump")).toMatchObject({ status: "fail" });

  const borderline = checkRoundFromArtifact({ ...CHECKS_ROUND_2, checks: [{ id: "audio_effects", status: "pass", detail: "borderline, not worth a revision: an effect is 3 LU over the bed" }], passed: undefined, failed: undefined }, 2);
  expect(borderline?.checks[0]?.status).toBe("borderline");
  expect(borderline?.passed).toBe(1);
  expect(checkRoundFromArtifact({ record_version: 2, checks: [] }, 1)).toBeUndefined();
});

test("the head of a plan_edit call and its answer give the plan as far as the 400-byte summaries go", () => {
  const plan = planFromSummaries(text(14), text(15));
  expect(plan).toMatchObject({
    contentType: "workout",
    playbook: "workout",
    story: "Gino's back day in 25 seconds: three pulls, one posing finish.",
    tempoBpm: 124,
    targetLengthS: 25,
    estimatedLengthMs: 24_200,
    shotCount: 6,
  });
  expect(plan?.hook).toBe("The heaviest lat pulldown lockout, punched in on the first beat.");
  expect(plan?.arc).toBe("Hook on the lockout, build through the rows, peak on the pulldown, end on the pose.");
  expect(plan?.ending).toBe("Hold the pose and loop back to the lockout.");
  // The answer is cut inside the third shot: the first two are read whole, the rest is counted but not known.
  expect(plan?.shots).toEqual([
    { role: "hook", beats: 3, lengthMs: 1450, effect: "punch_in" },
    { role: "build", beats: 4, lengthMs: 1940 },
  ]);
});

test("a string that the summary cut in two is not claimed", () => {
  const cut = 'plan_edit {"content_type":"workout","story":"A short one.","hook":"The heaviest lat pulldown lock…';
  const plan = planFromSummaries(cut, undefined);
  expect(plan).toMatchObject({ contentType: "workout", story: "A short one." });
  expect(plan?.hook).toBeUndefined();
});

test("whole shot lines are read, with and without an effect, and a shot cut in two is not", () => {
  const answer = "Plan saved (it is shown to the creator). Content type vlog, playbook vlog.v1. No beat grid: shots are counted in half seconds (500 ms a beat). Shot list, 3 shots, 6.0 s in all (target 6 s): 1. hook: 2 beats = 1.00 s, flash 2. build: 4 beats = 2.00 s 3. ending: 6 beats = 3.0…";
  const plan = planFromSummaries(undefined, answer);
  expect(plan?.tempoBpm).toBeNull();
  expect(plan?.shots).toEqual([
    { role: "hook", beats: 2, lengthMs: 1000, effect: "flash" },
    { role: "build", beats: 4, lengthMs: 2000 },
  ]);
  expect(plan?.shotCount).toBe(3);
  expect(planFromSummaries(undefined, "Something else")).toBeUndefined();
});

test("a check_edit answer gives the counts and the checks that fit in the summary", () => {
  const round = checkRoundFromSummary(text(22), 1);
  expect(round).toMatchObject({ nth: 1, version: 1, contentType: "workout", targetLengthS: 25, passed: 12, failed: 1, skipped: 0 });
  expect(round?.checks.length).toBeGreaterThan(0);
  expect(round?.checks.length).toBeLessThan(13);
  expect(round?.checks[0]).toMatchObject({ id: "hook_length", status: "pass", detail: "the first shot is 1.47 s long (at most 2.00 s)" });
  // The check that was cut in two is not read half way.
  expect(round?.checks.at(-1)?.detail?.endsWith("…")).toBe(false);
});

test("a short check_edit answer is read whole, with fail, n/a and borderline", () => {
  const answer = summarize(
    "check_edit for version 3 (talking): 1 of 2 checks pass, 1 fail, 1 do not apply.\nPASS hook_length: ok\nFAIL beat_sync: 0 of 4 cuts are on a beat\nn/a  rep_boundaries: no clip has reps\nPASS audio_effects: borderline, not worth a revision: close to its bar",
  );
  const round = checkRoundFromSummary(answer, 2);
  expect(round).toMatchObject({ nth: 2, version: 3, contentType: "talking", passed: 1, failed: 1, skipped: 1 });
  expect(round?.targetLengthS).toBeUndefined();
  expect(round?.checks.map((check) => [check.id, check.status])).toEqual([
    ["hook_length", "pass"],
    ["beat_sync", "fail"],
    ["rep_boundaries", "skip"],
    ["audio_effects", "borderline"],
  ]);
  expect(checkRoundFromSummary("check_edit failed", 1)).toBeUndefined();
});

test("a brightness match says how many clips it changed, or that there was nothing to do", () => {
  expect(matchFromSummary(text(25))).toEqual({ clips: 2 });
  expect(matchFromSummary("Nothing to change: the 3 measured clips are within 0.15 stops")).toEqual({ clips: 0 });
  expect(matchFromSummary("Nothing to match: fewer than two clips")).toEqual({ clips: 0 });
  expect(matchFromSummary("Matched the brightness of 1 clip toward p90 luma 0.20")).toEqual({ clips: 1 });
  expect(matchFromSummary("something else")).toBeUndefined();
});

test("the v2 run gives its plan and two rounds with the brightness match between them", () => {
  const artifacts = runArtifacts(events);
  expect(artifacts.plan?.contentType).toBe("workout");
  expect(artifacts.rounds.map((round) => [round.nth, round.passed, applicable(round), round.fixes])).toEqual([
    [1, 12, 13, []],
    [2, 13, 13, ["match_exposure"]],
  ]);
});

test("edits between two checks are a fix; the editor's first pass before the first check is not", () => {
  const sequence = [
    event(1, "tool_call", "edit_timeline {}", { tool: "edit_timeline" }),
    event(2, "tool_result", "ok", { tool: "edit_timeline" }),
    event(3, "tool_call", "check_edit {}", { tool: "check_edit" }),
    event(4, "tool_result", checkText(CHECKS_ROUND_1), { tool: "check_edit" }),
    event(5, "tool_call", "edit_timeline {}", { tool: "edit_timeline" }),
    event(6, "tool_result", "ok", { tool: "edit_timeline" }),
    event(7, "tool_call", "edit_timeline {}", { tool: "edit_timeline" }),
    event(8, "tool_result", "ok", { tool: "edit_timeline" }),
    event(9, "tool_call", "check_edit {}", { tool: "check_edit" }),
    event(10, "tool_result", checkText(CHECKS_ROUND_2), { tool: "check_edit" }),
  ];
  expect(runArtifacts(sequence).rounds.map((round) => round.fixes)).toEqual([[], ["edit"]]);
});

test("a refused plan_edit is no plan, and a check that failed to run is no round", () => {
  const sequence = [
    event(1, "tool_call", 'plan_edit {"content_type":"workout"}', { tool: "plan_edit" }),
    event(2, "tool_result", "the plan is not valid: story is empty", { tool: "plan_edit", is_error: true }),
    event(3, "tool_call", "check_edit {}", { tool: "check_edit" }),
    event(4, "tool_result", "the preview is not ready", { tool: "check_edit", is_error: true }),
  ];
  expect(runArtifacts(sequence)).toEqual({ rounds: [] });
});

test("the last accepted plan is the plan", () => {
  const first = events.filter((e) => e.seq === 14 || e.seq === 15);
  const second = [event(40, "tool_call", 'plan_edit {"content_type":"vlog","story":"A walk."}', { tool: "plan_edit" }), event(41, "tool_result", "Plan saved (it is shown to the creator). Content type vlog, playbook vlog.v1. No beat grid: shots are counted in half seconds.", { tool: "plan_edit" })];
  expect(runArtifacts([...first, ...second]).plan).toMatchObject({ contentType: "vlog", story: "A walk.", tempoBpm: null });
});
