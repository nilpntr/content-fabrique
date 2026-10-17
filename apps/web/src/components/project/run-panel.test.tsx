import { act, cleanup, fireEvent, screen } from "@testing-library/react";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { apiError, installApi, json, renderApp } from "@/test-api";
import { assetId, event, run, runId } from "@/test-fixtures";
import { RunPanel } from "./run-panel";

const titles = new Map([[assetId(2), "Row 2"]]);
const RUN = `GET /v1/runs/{id}`;

beforeEach(() => vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval"] }));
afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

/** Lets pending requests and their renders finish, then moves the clock. */
async function tick(ms = 0) {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms);
  });
}

const view = () => renderApp(<RunPanel runId={runId(1)} assetTitles={titles} />);

test("a run is followed while it is active: queued, running with its steps and cost, then done with the editor's summary, and polling stops", async () => {
  let polls = 0;
  const answers = [
    run({ status: "queued" }),
    run({
      status: "running",
      cost_micros: 120_000,
      active_ms: 65_000,
      events: [event(1, "status", "running"), event(2, "tool_call", `view_clip {"asset_id":"${assetId(2)}"}`, { tool: "view_clip" })],
      last_seq: 2,
    }),
    run({
      status: "succeeded",
      cost_micros: 410_000,
      active_ms: 98_000,
      summary: "A 12 second vertical cut: the pose, the row, the lockout.",
      timeline_version: 1,
      events: [event(3, "tool_call", 'edit_timeline {"base_version":0,"ops":[', { tool: "edit_timeline" }), event(4, "tool_call", "finish {}", { tool: "finish" })],
      last_seq: 4,
    }),
  ];
  const api = installApi({ [RUN]: () => answers[Math.min(polls++, answers.length - 1)] });
  view();
  await tick();
  expect(screen.getByRole("status").textContent).toBe("Waiting for the editor to start…");
  expect(screen.getByRole("heading").textContent).toBe("The editor is working");
  expect(screen.getByRole("button", { name: "Stop the editor" })).toBeTruthy();

  await tick(1600);
  expect(screen.getByRole("status").textContent).toBe("Looking at Row 2…");
  expect(screen.getByText("$0.12")).toBeTruthy();
  expect(screen.getByText("of $5.00")).toBeTruthy();
  expect(screen.getByText("1m 05s")).toBeTruthy();

  await tick(1600);
  expect(screen.getByRole("heading").textContent).toBe("The editor finished");
  expect(screen.getByTestId("run-summary").textContent).toBe("A 12 second vertical cut: the pose, the row, the lockout.");
  expect(screen.queryByRole("button", { name: "Stop the editor" })).toBeNull();
  // The events of every poll are kept: three tool calls. The run is over, so it is not "so far" any more.
  expect(screen.getByText("3 steps")).toBeTruthy();
  expect(screen.queryByText(/so far/)).toBeNull();

  const before = api.callsTo(RUN).length;
  await tick(10_000);
  expect(api.callsTo(RUN)).toHaveLength(before);
});

test("each poll asks only for the events after the ones it has", async () => {
  let polls = 0;
  const api = installApi({
    [RUN]: () =>
      polls++ === 0
        ? run({ status: "running", events: [event(1, "status", "running"), event(2, "tool_call", "get_brief {}", { tool: "get_brief" })], last_seq: 2 })
        : run({ status: "running", events: [event(3, "tool_call", "list_footage {}", { tool: "list_footage" })], last_seq: 3 }),
  });
  view();
  await tick();
  await tick(1600);
  expect(api.callsTo(RUN).map((call) => call.query.get("after_seq"))).toEqual(["0", "2"]);
  expect(screen.getByText("2 steps so far")).toBeTruthy();
});

test("a run that waits shows its question; the answer is sent and the run goes on", async () => {
  let answered = false;
  const api = installApi({
    [RUN]: () => (answered ? run({ status: "running" }) : run({ status: "waiting_input", question: "Which clip should open the video?" })),
    "POST /v1/runs/{id}/reply": () => {
      answered = true;
      return run({ status: "queued" });
    },
  });
  view();
  await tick();
  expect(screen.getByRole("heading").textContent).toBe("The editor has a question");
  expect(screen.getByRole("status").textContent).toBe("Waiting for your answer");
  const box = screen.getByLabelText("Which clip should open the video?");
  const send = screen.getByRole("button", { name: "Send answer" });
  expect(send.hasAttribute("disabled")).toBe(true);

  fireEvent.change(box, { target: { value: "  The back pose  " } });
  fireEvent.click(send);
  await tick();
  expect(api.callsTo("POST /v1/runs/{id}/reply")[0]?.body).toEqual({ text: "The back pose" });
  expect(screen.queryByLabelText("Which clip should open the video?")).toBeNull();
  expect(screen.getByRole("heading").textContent).toBe("The editor is working");
});

test("an answer the server refuses says why and keeps the text", async () => {
  installApi({
    [RUN]: run({ status: "waiting_input", question: "Which clip?" }),
    "POST /v1/runs/{id}/reply": apiError(409, "run_not_waiting"),
  });
  view();
  await tick();
  fireEvent.change(screen.getByLabelText("Which clip?"), { target: { value: "The first" } });
  fireEvent.click(screen.getByRole("button", { name: "Send answer" }));
  await tick();
  expect(screen.getByRole("alert").textContent).toMatch(/not waiting for an answer any more/);
  expect((screen.getByLabelText("Which clip?") as HTMLTextAreaElement).value).toBe("The first");
});

test("Stop cancels the run and says it is stopping until the run has stopped", async () => {
  let cancelled = false;
  const api = installApi({
    [RUN]: () => (cancelled ? run({ status: "cancelled", events: [], active_ms: 20_000 }) : run({ status: "running" })),
    "POST /v1/runs/{id}/cancel": () => {
      cancelled = false;
      // A running run stops between two steps: the answer may still say running.
      return run({ status: "running" });
    },
  });
  view();
  await tick();
  fireEvent.click(screen.getByRole("button", { name: "Stop the editor" }));
  await tick();
  expect(api.callsTo("POST /v1/runs/{id}/cancel")).toHaveLength(1);
  const stopping = screen.getByRole("button", { name: "Stopping the editor" });
  expect(stopping.hasAttribute("disabled")).toBe(true);

  cancelled = true;
  await tick(1600);
  expect(screen.getByRole("heading").textContent).toBe("You stopped the editor");
  expect(screen.queryByRole("button", { name: /Stop/ })).toBeNull();
  expect(screen.getByText(/kept as versions/)).toBeTruthy();
});

test("a failed run says why in plain words, with the limit it hit and the editor's last words", async () => {
  installApi({
    [RUN]: run({
      status: "failed",
      failure_reason: "budget",
      summary: "I made v2 but ran out of budget before checking it.",
      events: [event(5, "status", "failed reason=budget limit=dollars")],
      cost_micros: 5_000_000,
    }),
  });
  view();
  await tick();
  expect(screen.getByRole("heading").textContent).toBe("The editor stopped");
  const alert = screen.getByRole("alert");
  expect(alert.textContent).toContain("The editor reached its limit before it finished.");
  expect(alert.textContent).toContain("It hit its spending cap.");
  expect(alert.textContent).toContain("ran out of budget");
  expect(screen.queryByRole("button", { name: /Stop/ })).toBeNull();
});

test("a run that cannot be found, or loaded, says so and can be asked again", async () => {
  const api = installApi({ [RUN]: apiError(404, "not_found") });
  view();
  await tick();
  expect(screen.getByRole("alert").textContent).toBe("This editor run could not be found.");
  api.on(RUN, json(200, run({ status: "succeeded", summary: "Done." })));
  fireEvent.click(screen.getByRole("button", { name: "Try again" }));
  await tick();
  expect(screen.getByRole("heading").textContent).toBe("The editor finished");
});
