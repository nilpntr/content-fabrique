import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { installApi, renderApp } from "@/test-api";
import { assetId, CHECKS_ROUND_1, CHECKS_ROUND_2, event, PLAN_PAYLOAD, runId, v2Run } from "@/test-fixtures";
import { checkRoundFromArtifact, planFromArtifact } from "./run-artifacts";
import { EditChecksCard } from "./run-checks";
import { RunPanel, RunView } from "./run-panel";
import { CreativePlanCard } from "./run-plan-card";
import { RunStepList } from "./run-step-list";
import { runSteps } from "./run-steps";
import { FinishSummary, modelName, RunCostLine, summaryBlocks } from "./run-summary";

const titles = new Map([
  [assetId(1), "Lat pulldown"],
  [assetId(2), "Seated row"],
  [assetId(3), "Posing"],
]);

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

const noop = () => {};

// ---- The whole feed, from the API's own summaries -----------------------------------------------------------------------

// With no artifacts endpoint to ask (an older server: 404), the cards are made from what the event summaries carry.
test("the run feed of a v2 run, from the summaries alone, shows the plan, the self-review in two rounds, the finish and what the run used", async () => {
  installApi({ "GET /v1/runs/{id}": v2Run() });
  renderApp(<RunPanel runId={runId(1)} assetTitles={titles} />);
  await tick();

  // The plan: the kind of video, the story and its parts, the tempo and the length, and the shots the summaries carry.
  const plan = within(screen.getByRole("region", { name: "Creative plan" }));
  expect(plan.getByText("Workout", { selector: "[data-slot=content-type]" })).toBeTruthy();
  expect(plan.getByText("Workout playbook")).toBeTruthy();
  expect(plan.getByText("Gino's back day in 25 seconds: three pulls, one posing finish.")).toBeTruthy();
  expect(plan.getByText("The heaviest lat pulldown lockout, punched in on the first beat.")).toBeTruthy();
  expect(plan.getByText("124 BPM")).toBeTruthy();
  expect(plan.getByText(/25 s target/)).toBeTruthy();
  expect(plan.getAllByRole("listitem").filter((item) => item.textContent?.includes("beats"))).toHaveLength(2);
  expect(plan.getByText("+ 4 more shots")).toBeTruthy();

  // The self-review: the latest round on show, the round trail with what ran between the checks.
  const checks = within(screen.getByRole("region", { name: "Self-review" }));
  expect(checks.getByText("13 of 13 pass")).toBeTruthy();
  const rounds = within(checks.getByRole("list", { name: "Rounds" }));
  expect(rounds.getByRole("button", { name: "Check 1: 12 of 13 pass" }).textContent).toBe("check 112/13");
  expect(rounds.getByRole("button", { name: "Check 2: 13 of 13 pass" }).getAttribute("aria-pressed")).toBe("true");
  expect(rounds.getByText("match_exposure")).toBeTruthy();

  fireEvent.click(rounds.getByRole("button", { name: "Check 1: 12 of 13 pass" }));
  expect(checks.getByText("12 of 13 pass")).toBeTruthy();
  expect(checks.getByText("1 check needs work")).toBeTruthy();

  // The finish, the cost line.
  expect(screen.getByTestId("run-summary").textContent).toContain("A 24 second vertical cut of Gino's back day");
  expect(screen.getAllByRole("listitem").filter((item) => item.textContent?.startsWith("Opens on the heaviest"))).toHaveLength(1);
  expect(screen.getByText("Cut v2")).toBeTruthy();
  const cost = within(screen.getByRole("list", { name: "What this run used" }));
  expect(cost.getByText("Opus 5.5")).toBeTruthy();
  expect(cost.getByText("1m 38s")).toBeTruthy();
  expect(cost.getByText("16 tool calls")).toBeTruthy();
  expect(cost.getByText("$0.22")).toBeTruthy();
  expect(cost.getByText("of $5.00")).toBeTruthy();
  // The run was charged for its first edit.
  expect(cost.getByText("6 credits")).toBeTruthy();
});

test("a run that took no credits (released, or on a workspace without them) has no credits on its cost line", async () => {
  installApi({ "GET /v1/runs/{id}": v2Run({ credits: null }) });
  renderApp(<RunPanel runId={runId(1)} assetTitles={titles} />);
  await tick();
  expect(screen.getByTestId("run-cost").querySelector("[data-slot=credits]")).toBeNull();
});

test("a finished run folds its steps away, and they open to the timeline of what the editor did", async () => {
  installApi({ "GET /v1/runs/{id}": v2Run() });
  const { container } = renderApp(<RunPanel runId={runId(1)} assetTitles={titles} />);
  await tick();
  const details = container.querySelector("details") as HTMLDetailsElement;
  expect(details.open).toBe(false);
  expect(screen.getByText("12 steps")).toBeTruthy();

  fireEvent.click(screen.getByText("12 steps"));
  expect(details.open).toBe(true);
  const list = within(screen.getByRole("list", { name: "Steps" }));
  expect(list.getAllByRole("listitem").map((item) => item.getAttribute("data-kind"))).toEqual([
    "brief",
    "footage",
    "find_moments",
    "view_clip",
    "plan",
    "edit",
    "preview",
    "check",
    "match_exposure",
    "preview",
    "check",
    "finish",
  ]);
  expect(list.getByText("Looking at Lat pulldown, Seated row and Posing")).toBeTruthy();
  expect(list.getByText(/12 of 13 pass, 1 to fix/)).toBeTruthy();
});

test("a run that is working keeps its steps open and shows the one it is on spinning", async () => {
  const events = [event(1, "status", "running"), event(2, "tool_call", "get_brief {}", { tool: "get_brief" }), event(3, "tool_result", "x", { tool: "get_brief" }), event(4, "tool_call", "render_preview {}", { tool: "render_preview" })];
  installApi({ "GET /v1/runs/{id}": v2Run({ status: "running", events, summary: null, timeline_version: null, last_seq: 4 }) });
  const { container } = renderApp(<RunPanel runId={runId(1)} assetTitles={titles} />);
  await tick();
  expect((container.querySelector("details") as HTMLDetailsElement).open).toBe(true);
  expect(screen.getByText("2 steps so far")).toBeTruthy();
  expect(container.querySelector('[data-pending="true"]')?.textContent).toBe("Rendering a preview…");
  expect(screen.queryByTestId("run-summary")).toBeNull();
  // The creator's own choice wins over the run's state.
  fireEvent.click(screen.getByText("2 steps so far"));
  expect((container.querySelector("details") as HTMLDetailsElement).open).toBe(false);
});

test("a run with nothing to show but its steps has no cards", () => {
  render(<RunView run={v2Run({ status: "running", events: [event(1, "tool_call", "get_brief {}", { tool: "get_brief" })], summary: null })} assetTitles={titles} onCancel={noop} onReply={noop} />);
  expect(screen.queryByRole("region", { name: "Creative plan" })).toBeNull();
  expect(screen.queryByRole("region", { name: "Self-review" })).toBeNull();
});

// ---- The cards, with the full payloads -----------------------------------------------------------------------------------

test("the plan card shows the whole plan: tempo, target, story, and the shot list with clip titles, effects and beats", () => {
  const plan = planFromArtifact(PLAN_PAYLOAD);
  if (!plan) throw new Error("the plan payload was not read");
  render(<CreativePlanCard plan={plan} assetTitles={titles} />);
  const card = within(screen.getByTestId("creative-plan"));
  expect(card.getByText("Pace", { exact: false }).textContent).toBe("building pace");
  expect(card.getByText(/24\.2 s planned/)).toBeTruthy();
  for (const label of ["Hook", "Arc", "Ending"]) expect(card.getByText(label)).toBeTruthy();

  const shots = within(card.getByRole("list", { name: "" })).getAllByRole("listitem");
  expect(shots).toHaveLength(6);
  // Number, role, the clip's title, the effect, the length in beats.
  expect(shots[0]?.textContent).toContain("1");
  expect(shots[0]?.textContent).toContain("hook");
  expect(shots[0]?.textContent).toContain("Lat pulldown");
  expect(shots[0]?.textContent).toContain("Punch-in");
  expect(shots[0]?.textContent).toContain("3 beats");
  expect(shots[2]?.textContent).toContain("Speed ramp");
  expect(shots[3]?.textContent).toContain("Whip");
  expect(shots[5]?.textContent).toContain("Flash");
  expect(shots[5]?.textContent).toContain("8 beats");
  expect(card.queryByText(/more shots?/)).toBeNull();
});

test("a plan without a beat grid says so, and a plan known in part is a shorter card", () => {
  render(<CreativePlanCard plan={{ contentType: "talking", tempoBpm: null, shots: [] }} />);
  const card = within(screen.getByTestId("creative-plan"));
  expect(card.getByText("Talking head")).toBeTruthy();
  expect(card.getByText("No beat grid")).toBeTruthy();
  expect(card.queryByText("Hook")).toBeNull();
  expect(card.queryByText("Shot list", { exact: false })).toBeNull();
});

test("the checks card shows a chip per check with its reason on demand, and the failing one is marked", () => {
  const round = checkRoundFromArtifact(CHECKS_ROUND_1, 1);
  if (!round) throw new Error("the round was not read");
  render(<EditChecksCard rounds={[round]} />);
  const card = within(screen.getByTestId("edit-checks"));
  expect(card.getByText("12 of 13 pass")).toBeTruthy();
  const chips = card.getByRole("list", { name: "Checks of round 1" });
  expect(within(chips).getAllByRole("button")).toHaveLength(13);
  const fail = within(chips).getByRole("button", { name: "Even brightness: Fail" });
  expect(fail.getAttribute("data-status")).toBe("fail");
  expect(fail.getAttribute("title")).toContain("jumps 0.31 in brightness");

  expect(card.getByTestId("check-detail").textContent).toMatch(/Select a check/);
  fireEvent.click(fail);
  expect(fail.getAttribute("aria-expanded")).toBe("true");
  expect(card.getByTestId("check-detail").textContent).toContain("Even brightness. the cut from c3 to c4 jumps 0.31 in brightness: call match_exposure");
  // A second click closes it.
  fireEvent.click(fail);
  expect(card.getByTestId("check-detail").textContent).toMatch(/Select a check/);
});

test("a borderline check, a check that does not apply and a round known in part", () => {
  render(
    <EditChecksCard
      rounds={[
        {
          nth: 1,
          passed: 2,
          failed: 0,
          skipped: 1,
          fixes: [],
          checks: [
            { id: "audio_effects", status: "borderline", detail: "borderline, not worth a revision: close to its bar" },
            { id: "rep_boundaries", status: "skip", detail: "no clip has reps" },
          ],
        },
      ]}
    />,
  );
  expect(screen.getByRole("button", { name: "Sound effects: Borderline" }).getAttribute("data-status")).toBe("borderline");
  expect(screen.getByRole("button", { name: "Cuts on reps: Not applicable" }).getAttribute("data-status")).toBe("skip");
  // 2 passing and 1 not applicable make 3 checks; two are known.
  expect(screen.getByText("+ 1 more")).toBeTruthy();
  expect(screen.getByText("2 of 2 pass")).toBeTruthy();
});

test("the checks card follows the newest round until a round is picked", () => {
  const one = checkRoundFromArtifact(CHECKS_ROUND_1, 1);
  const two = checkRoundFromArtifact(CHECKS_ROUND_2, 2);
  if (!one || !two) throw new Error("rounds were not read");
  const { rerender } = render(<EditChecksCard rounds={[one]} />);
  expect(screen.queryByRole("list", { name: "Rounds" })).toBeNull();
  rerender(<EditChecksCard rounds={[one, { ...two, fixes: ["edit", "match_exposure"] }]} />);
  expect(screen.getByText("13 of 13 pass")).toBeTruthy();
  expect(screen.getByText("re-edit + match_exposure")).toBeTruthy();
});

// ---- The finish and the cost -----------------------------------------------------------------------------------------------

test("the editor's closing message becomes paragraphs and lists, and is never markup", () => {
  expect(summaryBlocks("One line.\nSame paragraph.\n\nSecond.\n- a\n- b\n\n1. first\n2) second")).toEqual([
    { kind: "p", text: "One line. Same paragraph." },
    { kind: "p", text: "Second." },
    { kind: "list", ordered: false, items: ["a", "b"] },
    { kind: "list", ordered: true, items: ["first", "second"] },
  ]);
  const { container } = render(<FinishSummary text={'<img src=x onerror="alert(1)"> **loud**'} version={3} />);
  expect(container.querySelector("img")).toBeNull();
  expect(screen.getByTestId("run-summary").textContent).toBe('<img src=x onerror="alert(1)"> **loud**');
  expect(screen.getByText("Cut v3")).toBeTruthy();
});

test("the cost line shows the credits of a run only when it is given them", () => {
  const run = v2Run();
  const { rerender } = render(<RunCostLine run={run} />);
  expect(screen.getByTestId("run-cost").querySelector("[data-slot=credits]")).toBeNull();
  rerender(<RunCostLine run={run} credits={{ state: "charged", action: "edit.first", credits: 6 }} />);
  expect(screen.getByText("6 credits")).toBeTruthy();
  rerender(<RunCostLine run={run} credits={{ state: "charged", action: "edit.revision", credits: 2 }} />);
  expect(screen.getByText("2 credits · revision")).toBeTruthy();
  rerender(<RunCostLine run={run} credits={{ state: "held", action: "edit.first", credits: 6 }} />);
  expect(screen.getByText("Holding 6 credits…")).toBeTruthy();
  rerender(<RunCostLine run={run} credits={{ state: "refunded", action: "edit.first", credits: 6 }} />);
  expect(screen.getByText("Refunded")).toBeTruthy();
  expect(screen.getByTestId("run-cost").querySelector("[data-slot=credits]")?.getAttribute("title")).toBe("6 credits given back");
  rerender(<RunCostLine run={{ ...run, tool_calls: 1 }} credits={{ state: "charged", action: "edit.first", credits: 1 }} />);
  expect(screen.getByText("1 tool call")).toBeTruthy();
  expect(screen.getByText("1 credit")).toBeTruthy();
});

test("a model id reads as a name", () => {
  expect(modelName("claude-opus-5-5")).toBe("Opus 5.5");
  expect(modelName("claude-haiku-5")).toBe("Haiku 5");
  expect(modelName("claude-haiku")).toBe("Haiku");
  expect(modelName("claude-sonnet-4-5-20250929")).toBe("Sonnet 4.5");
  expect(modelName("claude-opus-4-20250514")).toBe("Opus 4");
  expect(modelName("claude-3-5-sonnet-20241022")).toBe("Sonnet 3.5");
  expect(modelName("gpt-something")).toBe("gpt-something");
});

test("the step list marks errors and notes and scrolls to the newest step while the run works", () => {
  const steps = runSteps(
    [event(1, "tool_call", "edit_timeline {}", { tool: "edit_timeline" }), event(2, "tool_result", "bad", { tool: "edit_timeline", is_error: true }), event(3, "status", "limit_reached limit=dollars")],
    titles,
  );
  const { container } = render(<RunStepList steps={steps} active />);
  expect([...container.querySelectorAll("li")].map((item) => item.getAttribute("data-tone"))).toEqual(["normal", "error", "note"]);
});
