import { QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { ApiClientProvider } from "@/lib/api/provider";
import { createQueryClient } from "@/lib/query-client";
import { apiError, installApi, renderApp } from "@/test-api";
import { assetId, CHECKS_ROUND_1, checkText, event, runId, v2Artifacts, v2Run, v2RunEvents } from "@/test-fixtures";
import { checkRoundFromArtifact, runArtifacts } from "./run-artifacts";
import { EditChecksCard } from "./run-checks";
import { RunPanel } from "./run-panel";
import { runSteps } from "./run-steps";

const titles = new Map([[assetId(1), "Lat pulldown"]]);
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

test("a round is found by its check call: an artifact this reader cannot read leaves that round to its summary and does not shift the others", () => {
  const events = v2RunEvents();
  const [plan, round1, round2] = v2Artifacts().artifacts;
  // The artifact of the first check has no data; the second is whole.
  const broken = { ...(round1 as NonNullable<typeof round1>), data: null };
  const rounds = runArtifacts(events, [plan, broken, round2].filter((artifact) => artifact !== undefined)).rounds;
  expect(rounds.map((round) => [round.nth, round.version, round.passed, round.checks.length, round.fixes])).toEqual([
    [1, 1, 12, 4, []],
    [2, 2, 13, 13, ["match_exposure"]],
  ]);
});

test("a call with an id whose result has none is still paired, by order", () => {
  const mixed = [
    event(1, "tool_call", "check_edit {}", { tool: "check_edit", call_id: "a" }),
    event(2, "tool_result", checkText(CHECKS_ROUND_1), { tool: "check_edit" }),
    event(3, "tool_call", "render_preview {}", { tool: "render_preview", call_id: "b" }),
  ];
  expect(runArtifacts(mixed).rounds).toHaveLength(1);
  const steps = runSteps(mixed, titles);
  expect(steps.map((step) => [step.kind, step.pending])).toEqual([
    ["check", undefined],
    ["preview", true],
  ]);
});

test("an older server with no artifacts endpoint is asked once, not on every poll of the run", async () => {
  const api = installApi({ [RUN]: v2Run({ status: "running", summary: null, events: v2RunEvents().filter((e) => e.seq <= 16) }), [ARTIFACTS]: apiError(404, "not_found") });
  renderApp(<RunPanel runId={runId(1)} assetTitles={titles} />);
  await tick(50);
  await tick(50);
  await tick(6000);
  expect(api.callsTo(ARTIFACTS)).toHaveLength(1);
  // The run itself keeps being followed, and its plan comes from the summaries.
  expect(api.callsTo(RUN).length).toBeGreaterThan(2);
  expect(screen.getByRole("region", { name: "Creative plan" })).toBeTruthy();
});

test("a run that waits for an answer is followed at its own pace, not faster", async () => {
  const api = installApi({ [RUN]: v2Run({ status: "waiting_input", question: "Which?", summary: null, events: v2RunEvents().filter((e) => e.seq <= 16) }), [ARTIFACTS]: v2Artifacts() });
  renderApp(<RunPanel runId={runId(1)} assetTitles={titles} />);
  await tick(50);
  await tick(50);
  const before = api.callsTo(ARTIFACTS).length;
  await tick(2600);
  // 2.5 s for a run that waits: one more request in this time, not two.
  expect(api.callsTo(ARTIFACTS).length - before).toBe(1);
});

test("artifacts fetched while the run still worked are fetched again when its panel comes back after it finished", async () => {
  let finished = false;
  const all = v2Artifacts().artifacts;
  const api = installApi({
    [RUN]: () => (finished ? v2Run() : v2Run({ status: "running", summary: null, events: v2RunEvents().filter((e) => e.seq <= 16) })),
    [ARTIFACTS]: (call: { query: URLSearchParams }) => ({ artifacts: all.filter((artifact) => artifact.seq > Number(call.query.get("after_seq")) && (finished || artifact.seq <= 16)), next_after_seq: null }),
  });
  // One query cache for the whole visit, as in the app.
  const client = createQueryClient();
  const shown = () => (
    <QueryClientProvider client={client}>
      <ApiClientProvider>
        <RunPanel runId={runId(1)} assetTitles={titles} />
      </ApiClientProvider>
    </QueryClientProvider>
  );
  const first = render(shown());
  await tick(50);
  await tick(50);
  expect(screen.queryByRole("region", { name: "Self-review" })).toBeNull();
  // The creator leaves while the run works, and comes back when it has finished.
  first.unmount();
  finished = true;
  // Time passes: the cached run is out of date when the panel is shown again.
  await client.invalidateQueries({ queryKey: ["run"] });
  const before = api.callsTo(ARTIFACTS).length;
  render(shown());
  await tick(50);
  await tick(50);
  await tick(50);
  expect(api.callsTo(ARTIFACTS).length).toBeGreaterThan(before);
  expect(screen.getByText("13 of 13 pass")).toBeTruthy();
  // And now that they were fetched after the end, they are kept.
  const settled = api.callsTo(ARTIFACTS).length;
  cleanup();
  render(shown());
  await tick(5000);
  expect(api.callsTo(ARTIFACTS)).toHaveLength(settled);
});

test("folding a long round closes the reason of a chip that folds away", () => {
  const checks = Array.from({ length: 20 }, (_, i) => ({ id: `check_${i}`, status: i === 3 ? "fail" : "pass", detail: `reason ${i}` }));
  const round = checkRoundFromArtifact({ record_version: 1, version: 1, checks }, 1);
  if (!round) throw new Error("the round was not read");
  render(<EditChecksCard rounds={[round]} />);
  fireEvent.click(screen.getByRole("button", { name: "Show all 20 checks" }));
  fireEvent.click(screen.getByRole("button", { name: "Check 0: Pass" }));
  expect(screen.getByTestId("check-detail").textContent).toContain("reason 0");
  fireEvent.click(screen.getByRole("button", { name: "Show fewer" }));
  expect(screen.getByTestId("check-detail").textContent).toMatch(/Select a check/);
});
