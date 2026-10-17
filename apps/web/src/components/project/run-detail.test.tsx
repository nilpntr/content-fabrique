import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { useMe } from "@/lib/api/hooks";
import { useRun, useStartEdit } from "@/lib/api/project-hooks";
import { apiError, installApi, json, renderApp } from "@/test-api";
import { ME } from "@/test-clerk";
import { assetId, CHECKS_ROUND_1, CHECKS_ROUND_2, callIdOf, checkText, event, PLAN_PAYLOAD, projectId, run, runId, v2Artifacts, v2Run, v2RunEvents } from "@/test-fixtures";
import { applicable, checkRoundFromArtifact, hasArtifactEvents, planFromArtifact, runArtifacts } from "./run-artifacts";
import { EditChecksCard } from "./run-checks";
import { CreativePlanCard } from "./run-plan-card";
import { RunPanel } from "./run-panel";
import { runSteps } from "./run-steps";

const titles = new Map([
  [assetId(1), "Lat pulldown"],
  [assetId(2), "Seated row"],
  [assetId(3), "Posing"],
]);
const RUN = "GET /v1/runs/{id}";
const ARTIFACTS = "GET /v1/runs/{id}/artifacts";

beforeEach(() => vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval"] }));
afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

async function tick(ms = 0) {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms);
  });
}

// ---- The whole plan and self-review, from the artifacts endpoint ---------------------------------------------------------------

test("the plan card and the self-review show every shot and every check once the artifacts are fetched", async () => {
  const api = installApi({ [RUN]: v2Run(), [ARTIFACTS]: v2Artifacts() });
  renderApp(<RunPanel runId={runId(1)} assetTitles={titles} />);
  await tick(50);
  await tick(50);

  const plan = within(screen.getByRole("region", { name: "Creative plan" }));
  const shots = plan.getAllByRole("listitem").filter((item) => item.textContent?.includes("beats"));
  expect(shots).toHaveLength(6);
  expect(shots[0]?.textContent).toContain("Lat pulldown");
  expect(shots[5]?.textContent).toContain("Flash");
  expect(plan.queryByText(/more shots?/)).toBeNull();
  expect(plan.getByText("The heaviest lat pulldown lockout, punched in on the first beat.")).toBeTruthy();

  const checks = within(screen.getByRole("region", { name: "Self-review" }));
  expect(checks.getByText("13 of 13 pass")).toBeTruthy();
  expect(within(checks.getByRole("list", { name: "Checks of round 2" })).getAllByRole("button")).toHaveLength(13);
  expect(checks.queryByText(/\+ \d+ more/)).toBeNull();
  // What ran between the two checks, from the events, by the call id of the check.
  expect(within(checks.getByRole("list", { name: "Rounds" })).getByText("match_exposure")).toBeTruthy();
  // The first round has its failing check, with its reason.
  fireEvent.click(within(checks.getByRole("list", { name: "Rounds" })).getByRole("button", { name: "Check 1: 12 of 13 pass" }));
  const fail = within(checks.getByRole("list", { name: "Checks of round 1" })).getByRole("button", { name: "Even brightness: Fail" });
  fireEvent.click(fail);
  expect(checks.getByTestId("check-detail").textContent).toContain("jumps 0.31 in brightness");

  // One request for the artifacts, from the start, a page of the most the API gives.
  const asked = api.callsTo(ARTIFACTS);
  expect(asked).toHaveLength(1);
  expect(asked[0]?.query.get("after_seq")).toBe("0");
  expect(asked[0]?.query.get("limit")).toBe("50");
});

test("a run with no artifact events does not ask for artifacts, and a finished run asks once and not again", async () => {
  const api = installApi({ [RUN]: run({ status: "succeeded", summary: "Done.", events: [event(1, "tool_call", "get_brief {}", { tool: "get_brief" })] }), [ARTIFACTS]: v2Artifacts() });
  renderApp(<RunPanel runId={runId(1)} assetTitles={titles} />);
  await tick(5000);
  expect(api.callsTo(ARTIFACTS)).toHaveLength(0);

  const second = installApi({ [RUN]: v2Run(), [ARTIFACTS]: v2Artifacts() });
  cleanup();
  renderApp(<RunPanel runId={runId(1)} assetTitles={titles} />);
  await tick(10_000);
  expect(second.callsTo(ARTIFACTS)).toHaveLength(1);
});

test("when the artifacts cannot be fetched the cards are made from the summaries, and the run still shows", async () => {
  installApi({ [RUN]: v2Run(), [ARTIFACTS]: apiError(500, "internal_error") });
  renderApp(<RunPanel runId={runId(1)} assetTitles={titles} />);
  await tick(5000);
  const plan = within(screen.getByRole("region", { name: "Creative plan" }));
  expect(plan.getByText("+ 4 more shots")).toBeTruthy();
  expect(screen.getByText("13 of 13 pass")).toBeTruthy();
  expect(screen.getByTestId("run-summary")).toBeTruthy();
});

test("while the run works its artifacts are fetched again from the last one it has, and once more when it ends", async () => {
  const all = v2Artifacts().artifacts;
  let phase = 0;
  const api = installApi({
    [RUN]: () => {
      const events = v2RunEvents();
      if (phase === 0) return v2Run({ status: "running", summary: null, credits: { state: "held", action: "edit.first", credits: 6 }, events: events.filter((e) => e.seq <= 16) });
      if (phase === 1) return v2Run({ status: "running", summary: null, credits: { state: "held", action: "edit.first", credits: 6 }, events: events.filter((e) => e.seq <= 23) });
      return v2Run();
    },
    [ARTIFACTS]: (call: { query: URLSearchParams }) => {
      const after = Number(call.query.get("after_seq"));
      const upTo = phase === 0 ? 16 : phase === 1 ? 23 : 99;
      return { artifacts: all.filter((artifact) => artifact.seq > after && artifact.seq <= upTo), next_after_seq: null };
    },
  });
  renderApp(<RunPanel runId={runId(1)} assetTitles={titles} />);
  await tick(50);
  await tick(50);
  // The plan is there, with all its shots; no checks yet. The credits are held.
  expect(within(screen.getByRole("region", { name: "Creative plan" })).queryByText(/more shots?/)).toBeNull();
  expect(screen.queryByRole("region", { name: "Self-review" })).toBeNull();
  expect(screen.getByText("Holding 6 credits…")).toBeTruthy();

  phase = 1;
  await tick(1600);
  expect(screen.getByText("12 of 13 pass")).toBeTruthy();
  phase = 2;
  await tick(1600);
  await tick(1600);
  expect(screen.getByText("13 of 13 pass")).toBeTruthy();
  expect(screen.getByText("6 credits")).toBeTruthy();

  const afters = api.callsTo(ARTIFACTS).map((call) => call.query.get("after_seq"));
  expect(afters[0]).toBe("0");
  // Each later request starts after what is already there.
  expect(afters.slice(1).every((after) => Number(after) >= 16)).toBe(true);

  // The run is over: no more asking.
  const before = api.callsTo(ARTIFACTS).length;
  await tick(10_000);
  expect(api.callsTo(ARTIFACTS)).toHaveLength(before);
});

test("the pages of a long list of artifacts are followed to the end", async () => {
  const [first, ...rest] = v2Artifacts().artifacts;
  const api = installApi({
    [RUN]: v2Run(),
    [ARTIFACTS]: (call: { query: URLSearchParams }) => (call.query.get("after_seq") === "0" ? { artifacts: [first], next_after_seq: first?.seq ?? null } : { artifacts: rest, next_after_seq: null }),
  });
  renderApp(<RunPanel runId={runId(1)} assetTitles={titles} />);
  await tick();
  await tick();
  expect(api.callsTo(ARTIFACTS).map((call) => call.query.get("after_seq"))).toEqual(["0", "16"]);
  expect(screen.getByText("13 of 13 pass")).toBeTruthy();
});

// ---- Putting artifacts and events together ------------------------------------------------------------------------------------

test("the artifacts win over the summaries, and a round the events show but the artifacts do not have yet is kept", () => {
  const events = v2RunEvents();
  const [plan, round1] = v2Artifacts().artifacts;
  const onlyFirst = runArtifacts(events, [plan, round1].filter((artifact) => artifact !== undefined));
  expect(onlyFirst.plan?.shots).toHaveLength(6);
  expect(onlyFirst.rounds.map((round) => [round.nth, round.checks.length, round.fixes])).toEqual([
    [1, 13, []],
    // The second check is in the events (read from its summary, 4 checks known) and not in the artifacts yet.
    [2, 4, ["match_exposure"]],
  ]);
  const all = runArtifacts(events, v2Artifacts().artifacts);
  expect(all.rounds.map((round) => [round.nth, round.checks.length, applicable(round), round.fixes])).toEqual([
    [1, 13, 13, []],
    [2, 13, 13, ["match_exposure"]],
  ]);
});

test("an artifact with no data, or one this reader does not know, changes nothing", () => {
  const events = v2RunEvents();
  const [plan] = v2Artifacts().artifacts;
  const same = runArtifacts(events, [{ ...(plan as NonNullable<typeof plan>), data: null }, { seq: 40, kind: "edit_check", call_id: null, data: { record_version: 9 }, created_at: "" }]);
  expect(same.plan?.shots).toHaveLength(2);
  expect(same.rounds).toHaveLength(2);
});

test("calls, results and artifacts are paired by call id, so results that arrive out of order are not mixed up", () => {
  const events = [
    event(1, "tool_call", "check_edit {}", { tool: "check_edit", call_id: "a" }),
    event(2, "tool_call", "check_edit {}", { tool: "check_edit", call_id: "b" }),
    event(3, "tool_result", checkText({ ...CHECKS_ROUND_2, version: 2 }), { tool: "check_edit", call_id: "b" }),
    event(4, "tool_result", checkText({ ...CHECKS_ROUND_1, version: 1 }), { tool: "check_edit", call_id: "a" }),
  ];
  const rounds = runArtifacts(events).rounds;
  // The rounds are in the order the calls were made, each with its own result: a is the first check (12 of 13), b the second (13 of 13),
  // though b's result came first.
  expect(rounds.map((round) => [round.nth, round.version, round.passed])).toEqual([
    [1, 1, 12],
    [2, 2, 13],
  ]);
  const steps = runSteps(events, titles);
  expect(steps.map((step) => [step.detail, step.pending])).toEqual([
    ["12 of 13 pass, 1 to fix", undefined],
    ["13 of 13 pass", undefined],
  ]);
  // A call whose result has another id is still waiting.
  const waiting = runSteps([event(1, "tool_call", "render_preview {}", { tool: "render_preview", call_id: "x" }), event(2, "tool_result", "ok", { tool: "render_preview", call_id: "y" })], titles);
  expect(waiting[0]?.pending).toBe(true);
});

test("a run from before call ids is paired by order and gives the same plan and rounds", () => {
  const withIds = runArtifacts(v2RunEvents());
  const legacy = runArtifacts(v2RunEvents({ legacy: true }));
  expect(legacy).toEqual(withIds);
  expect(hasArtifactEvents(v2RunEvents({ legacy: true }))).toBe(true);
  expect(hasArtifactEvents([event(1, "status", "running")])).toBe(false);
  // Artifacts of an old run have no call id; the checks are matched by place.
  const noIds = v2Artifacts().artifacts.map((artifact) => ({ ...artifact, call_id: null }));
  expect(runArtifacts(v2RunEvents({ legacy: true }), noIds).rounds.map((round) => round.fixes)).toEqual([[], ["match_exposure"]]);
  expect(callIdOf(14)).toBe("toolu_0014");
});

// ---- A long shot list folds ----------------------------------------------------------------------------------------------------

test("a long shot list folds to its first shots, and opens on request", () => {
  const shots = Array.from({ length: 12 }, (_, i) => ({ role: i === 0 ? "hook" : "build", what: `Shot ${i + 1}`, asset_id: assetId(1), beats: 2, effect: "none", length_ms: 968 }));
  const plan = planFromArtifact({ ...PLAN_PAYLOAD, shots });
  if (!plan) throw new Error("the plan was not read");
  render(<CreativePlanCard plan={plan} assetTitles={titles} />);
  const rows = () => screen.getAllByRole("listitem").filter((item) => item.textContent?.includes("beats"));
  expect(rows()).toHaveLength(6);
  const toggle = screen.getByRole("button", { name: "Show all 12 shots" });
  expect(toggle.getAttribute("aria-expanded")).toBe("false");
  fireEvent.click(toggle);
  expect(rows()).toHaveLength(12);
  expect(screen.getByRole("button", { name: "Show fewer shots" }).getAttribute("aria-expanded")).toBe("true");
  fireEvent.click(screen.getByRole("button", { name: "Show fewer shots" }));
  expect(rows()).toHaveLength(6);
});

test("a very long round of checks folds to the ones that need a look, and opens on request; sixteen are shown whole", () => {
  const checks = (n: number) => Array.from({ length: n }, (_, i) => ({ id: `check_${i}`, status: i === 3 ? "fail" : "pass", detail: i === 5 ? "borderline, close to its bar" : `check ${i} is fine` }));
  const roundOf = (n: number) => checkRoundFromArtifact({ record_version: 1, version: 1, checks: checks(n) }, 1);
  const long = roundOf(20);
  if (!long) throw new Error("the round was not read");
  const { rerender } = render(<EditChecksCard rounds={[long]} />);
  const chips = () => within(screen.getByRole("list", { name: "Checks of round 1" })).getAllByRole("button").filter((button) => button.hasAttribute("data-check"));
  expect(chips().map((chip) => chip.getAttribute("data-status"))).toEqual(["fail", "borderline"]);
  fireEvent.click(screen.getByRole("button", { name: "Show all 20 checks" }));
  expect(chips()).toHaveLength(20);
  fireEvent.click(screen.getByRole("button", { name: "Show fewer" }));
  expect(chips()).toHaveLength(2);

  const sixteen = roundOf(16);
  if (!sixteen) throw new Error("the round was not read");
  rerender(<EditChecksCard rounds={[sixteen]} />);
  expect(chips()).toHaveLength(16);
  expect(screen.queryByRole("button", { name: /Show all/ })).toBeNull();
});

test("a plan of eight shots or fewer is shown whole, with no toggle", () => {
  const plan = planFromArtifact({ ...PLAN_PAYLOAD, shots: PLAN_PAYLOAD.shots.slice(0, 6) });
  if (!plan) throw new Error("the plan was not read");
  render(<CreativePlanCard plan={plan} />);
  expect(screen.queryByRole("button", { name: /Show/ })).toBeNull();
});

// ---- The balance in the header follows the run ------------------------------------------------------------------------------------

/** The page's run and the header's balance: the two observers that matter. */
function Settling({ id }: { id: string }) {
  useRun(id);
  useMe();
  return null;
}

test("the balance is read again when a run reaches a terminal state, and not while it works", async () => {
  let status: "running" | "succeeded" = "running";
  const api = installApi({ "GET /v1/me": ME, [RUN]: () => run({ status, model: "claude-opus-5-5", events: [] }) });
  // `me` is in the cache, as in the app, so that invalidating it refetches it.
  renderApp(<Settling id={runId(1)} />);
  await tick();
  const before = api.callsTo("GET /v1/me").length;
  expect(before).toBe(1);
  await tick(1600);
  expect(api.callsTo("GET /v1/me")).toHaveLength(before);

  status = "succeeded";
  await tick(1600);
  await tick();
  expect(api.callsTo("GET /v1/me").length).toBeGreaterThan(before);
});

function Starter() {
  const start = useStartEdit();
  useMe();
  return (
    <button type="button" onClick={() => start.mutate({ projectId: projectId(1), body: {} })}>
      start
    </button>
  );
}

test("starting an edit reads the balance again: the run's credits are held at once", async () => {
  const api = installApi({ "GET /v1/me": ME, "POST /v1/projects/{id}/edit": json(202, run({ status: "queued" })) });
  renderApp(<Starter />);
  await tick();
  const before = api.callsTo("GET /v1/me").length;
  expect(before).toBe(1);
  fireEvent.click(screen.getByRole("button", { name: "start" }));
  await tick();
  await tick();
  expect(api.callsTo("GET /v1/me").length).toBeGreaterThan(before);
});
