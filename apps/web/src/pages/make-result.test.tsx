import { cleanup, fireEvent, screen, waitFor, within } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { apiError, type FakeApi, installApi, json, renderApp } from "@/test-api";
import { ME } from "@/test-clerk";
import { clip, event, project, projectId, readyRender, render, run, runId, timelineVersion, v2Artifacts, v2Run, versionSummary } from "@/test-fixtures";
import { MakeResultPage } from "./make-result";

vi.mock("@tanstack/react-router", async (original) => ({
  ...(await original<typeof import("@tanstack/react-router")>()),
  Link: ({ to, children, className }: { to: string; children: ReactNode; className?: string }) => (
    <a href={to} className={className}>
      {children}
    </a>
  ),
}));

beforeEach(() => window.localStorage.clear());
afterEach(cleanup);

const P = projectId(1);
const R = runId(1);
const call = (seq: number, tool: string, text = tool) => event(seq, "tool_call", text, { tool });

/** A project with no video yet. The run, the videos and the renders are the test's. */
function api(routes: Record<string, object> = {}): FakeApi {
  return installApi({
    "GET /v1/me": ME,
    "GET /v1/assets": { items: [], next_cursor: null },
    "GET /v1/projects/{id}": project(1),
    "GET /v1/projects/{id}/timelines": { items: [], next_before: null },
    ...routes,
  });
}

/** A project whose edit made one video of two clips, which can be played. */
function withVideo(routes: Record<string, object> = {}): FakeApi {
  const spec = timelineVersion(1, [clip("c1", 1, 0, 0, 4000), clip("c2", 2, 4000, 0, 4000)]);
  return api({
    "GET /v1/projects/{id}/timelines": { items: [versionSummary(1, { author_id: R })], next_before: null },
    "GET /v1/projects/{id}/timelines/{version}": spec,
    "GET /v1/runs/{id}": run({ status: "succeeded", timeline_version: 1 }),
    "GET /v1/projects/{id}/renders": { items: [readyRender()] },
    "GET /v1/renders/{id}": readyRender(),
    ...routes,
  });
}

function page(props: Partial<Parameters<typeof MakeResultPage>[0]> = {}) {
  const onRunStarted = vi.fn();
  const onStartOver = vi.fn();
  renderApp(<MakeResultPage projectId={P} runId={R} onRunStarted={onRunStarted} onStartOver={onStartOver} {...props} />);
  return { onRunStarted, onStartOver };
}

const stepStates = () => Object.fromEntries(within(screen.getByRole("list", { name: "Steps" })).getAllByRole("listitem").map((item) => [item.textContent, item.getAttribute("data-state")]));

test("while it loads, the screen is grey blocks and says so to a screen reader", () => {
  api({ "GET /v1/projects/{id}": () => new Promise(() => {}) });
  page();
  expect(screen.getByRole("status").textContent).toBe("Loading your video");
  expect(document.querySelectorAll('[data-slot="skeleton"]').length).toBeGreaterThan(0);
});

test("while the editor works, the steps are in plain words with a word for where each is", async () => {
  api({ "GET /v1/runs/{id}": run({ status: "running", events: [call(1, "list_footage"), call(2, "find_moments"), call(3, "edit_timeline")] }) });
  page();
  expect(await screen.findByRole("heading", { name: "I'm making your video" })).toBeTruthy();
  expect(screen.getByRole("status").textContent).toBe("Cutting to the beat…");
  expect(stepStates()).toEqual({
    "Watching your clipsDone": "done",
    "Finding your best momentsDone": "done",
    "Cutting to the beatNow": "now",
    "Adding captions": "todo",
    "Checking it over": "todo",
  });
  expect(screen.getByText(/You can leave this page/)).toBeTruthy();
  // Nothing technical: no tool names, no costs.
  expect(document.body.textContent).not.toMatch(/edit_timeline|find_moments|\$\d|token/);
});

test("Stop stops the edit", async () => {
  const fake = api({ "GET /v1/runs/{id}": run({ status: "running" }) });
  fake.on("POST /v1/runs/{id}/cancel", () => {
    fake.on("GET /v1/runs/{id}", run({ status: "cancelled" }));
    return run({ status: "cancelled" });
  });
  page();
  fireEvent.click(await screen.findByRole("button", { name: "Stop" }));
  await waitFor(() => expect(fake.callsTo("POST /v1/runs/{id}/cancel")).toHaveLength(1));
  expect(await screen.findByRole("heading", { name: "You stopped this one" })).toBeTruthy();
});

test("a question from the editor is shown with a box, and the answer is sent", async () => {
  const fake = api({
    "GET /v1/runs/{id}": run({ status: "waiting_input", question: "Which clip should open the video?" }),
    "POST /v1/runs/{id}/reply": run({ status: "running" }),
  });
  page();
  expect(await screen.findByRole("heading", { name: "I have a question for you" })).toBeTruthy();
  fireEvent.change(screen.getByLabelText("Which clip should open the video?"), { target: { value: "The squat" } });
  fireEvent.click(screen.getByRole("button", { name: "Send answer" }));
  await waitFor(() => expect(fake.callsTo("POST /v1/runs/{id}/reply")[0]?.body).toEqual({ text: "The squat" }));
});

test("the finished video plays, with a numbered list of what is in it and three things to do", async () => {
  withVideo();
  page();
  expect(await screen.findByRole("heading", { name: "Here's your video" })).toBeTruthy();
  await waitFor(() => expect(screen.getByLabelText("Your video").getAttribute("src")).toBe("https://files.test/preview.mp4"));

  const shots = await screen.findByTestId("shot-list");
  expect(within(shots).getByRole("heading", { name: "What's in it" })).toBeTruthy();
  expect(within(shots).getAllByRole("listitem").map((item) => item.textContent)).toEqual(["11.Opens on your row 1", "22.Ends on your row 2"]);

  expect(within(screen.getByRole("group", { name: "What next?" })).getAllByRole("button").map((button) => button.textContent)).toEqual(["Looks great", "Change something", "Start over"]);
  expect(document.body.textContent).not.toMatch(/Version \d|timeline|render/i);
});

test("the shot list follows the editor's plan when the edit that made the video has one", async () => {
  withVideo({
    "GET /v1/projects/{id}/timelines": { items: [versionSummary(2, { author_id: R })], next_before: null },
    "GET /v1/projects/{id}/timelines/{version}": timelineVersion(2, [clip("c1", 1, 0, 0, 4000)]),
    "GET /v1/runs/{id}": v2Run(),
    "GET /v1/runs/{id}/artifacts": v2Artifacts(),
    "GET /v1/projects/{id}/renders": { items: [readyRender({ timeline_version: 2 })] },
    "GET /v1/renders/{id}": readyRender({ timeline_version: 2 }),
  });
  page();
  const shots = await screen.findByTestId("shot-list");
  await waitFor(() => expect(within(shots).getAllByRole("listitem").length).toBeGreaterThan(1));
  expect(within(shots).getAllByRole("listitem")[0]?.textContent).toMatch(/^11\.Opens on /);
});

test("'Looks great' finishes: the download, and the way to another video", async () => {
  withVideo();
  const { onStartOver } = page();
  fireEvent.click(await screen.findByRole("button", { name: "Looks great" }));
  const done = screen.getByRole("region", { name: "All done" });
  expect(within(done).getByRole("heading", { name: "All done. It's yours to post." })).toBeTruthy();
  expect(await within(done).findByRole("button", { name: "Download video" })).toBeTruthy();
  expect(within(done).getByRole("link", { name: "See all my videos" }).getAttribute("href")).toBe("/projects");
  fireEvent.click(within(done).getByRole("button", { name: "Make another video" }));
  expect(onStartOver).toHaveBeenCalled();
});

test("'Change something' takes quick changes and the creator's own words, and starts the next round", async () => {
  const fake = withVideo({ "POST /v1/projects/{id}/edit": json(202, run({ id: runId(2) })) });
  const { onRunStarted } = page();
  fireEvent.click(await screen.findByRole("button", { name: "Change something" }));

  const chips = within(screen.getByRole("group", { name: "Quick changes" }));
  expect(chips.getAllByRole("button").map((chip) => chip.textContent)).toEqual(["Shorter", "Longer", "More energy", "Different music"]);
  const send = screen.getByRole("button", { name: "Make the changes" }) as HTMLButtonElement;
  expect(send.disabled).toBe(true);

  fireEvent.click(chips.getByRole("button", { name: "Shorter" }));
  fireEvent.click(chips.getByRole("button", { name: "More energy" }));
  expect(chips.getByRole("button", { name: "Shorter" }).getAttribute("aria-pressed")).toBe("true");
  fireEvent.change(screen.getByLabelText("Or tell me in your own words"), { target: { value: "Open on the squat" } });
  fireEvent.click(send);

  await waitFor(() => expect(onRunStarted).toHaveBeenCalledWith(runId(2)));
  expect(fake.callsTo("POST /v1/projects/{id}/edit")[0]?.body).toEqual({ instructions: "Make it shorter. Give it more energy, with faster cuts. Open on the squat", vibe: "hype" });
});

test("a change the API refuses is said in a sentence, and the words are kept", async () => {
  withVideo({ "POST /v1/projects/{id}/edit": apiError(402, "credits_exhausted") });
  const { onRunStarted } = page();
  fireEvent.click(await screen.findByRole("button", { name: "Change something" }));
  fireEvent.click(screen.getByRole("button", { name: "Longer" }));
  fireEvent.click(screen.getByRole("button", { name: "Make the changes" }));
  const alert = await screen.findByRole("alert");
  expect(alert.textContent).toContain("You've used up your videos for now. See your plan in Settings.");
  expect(alert.textContent).not.toContain("credits_exhausted");
  expect(screen.getByRole("button", { name: "Longer" }).getAttribute("aria-pressed")).toBe("true");
  expect(onRunStarted).not.toHaveBeenCalled();
});

test("'Start over' goes back to the first screen", async () => {
  withVideo();
  const { onStartOver } = page();
  fireEvent.click(await screen.findByRole("button", { name: "Start over" }));
  expect(onStartOver).toHaveBeenCalled();
  expect(screen.getByText("Starting over keeps this video saved under Projects.")).toBeTruthy();
});

test("a video that is still being put together says how far it is", async () => {
  const working = render({ status: "rendering", progress: 40 });
  withVideo({ "GET /v1/projects/{id}/renders": { items: [working] }, "GET /v1/renders/{id}": working });
  page();
  await waitFor(() => expect(screen.getByRole("status").textContent).toBe("Putting your video together…40%"));
  expect(screen.queryByLabelText("Your video")).toBeNull();
});

test("a video nobody asked to see yet is asked for once", async () => {
  const fake = withVideo({
    "GET /v1/projects/{id}/renders": { items: [] },
    "POST /v1/projects/{id}/renders": json(202, render({ status: "queued" })),
    "GET /v1/renders/{id}": render({ status: "queued" }),
  });
  page();
  await waitFor(() => expect(fake.callsTo("POST /v1/projects/{id}/renders")).toHaveLength(1));
  expect(fake.callsTo("POST /v1/projects/{id}/renders")[0]?.body).toEqual({ version: 1, profile: "preview" });
  expect(await screen.findByText(/Putting your video together/)).toBeTruthy();
});

test("a video that cannot be shown says why, with the button to try again", async () => {
  const failed = render({ status: "failed", failure_reason: "timeout" });
  const fake = withVideo({
    "GET /v1/projects/{id}/renders": { items: [failed] },
    "GET /v1/renders/{id}": failed,
    "POST /v1/projects/{id}/renders": json(202, render({ id: failed.id, status: "queued" })),
  });
  page();
  const alert = await screen.findByRole("alert");
  expect(alert.textContent).toContain("I can't show the video right now");
  expect(alert.textContent).toContain("it took too long");
  fireEvent.click(within(alert).getByRole("button", { name: "Try again" }));
  await waitFor(() => expect(fake.callsTo("POST /v1/projects/{id}/renders")).toHaveLength(1));
});

test("an edit that stopped before any video says why in a sentence, that it cost nothing, and offers to try again", async () => {
  const fake = api({
    "GET /v1/runs/{id}": run({ status: "failed", failure_reason: "model_error" }),
    "POST /v1/projects/{id}/edit": json(202, run({ id: runId(2) })),
  });
  const { onRunStarted } = page();
  const alert = await screen.findByRole("alert");
  expect(alert.textContent).toContain("I couldn't finish your video");
  expect(alert.textContent).toContain("Something went wrong on my side. It didn't cost you anything.");
  expect(alert.textContent).not.toContain("model_error");
  fireEvent.click(within(alert).getByRole("button", { name: "Try again" }));
  await waitFor(() => expect(onRunStarted).toHaveBeenCalledWith(runId(2)));
  expect(fake.callsTo("POST /v1/projects/{id}/edit")[0]?.body).toEqual({});
});

test("when trying again cannot help, the one step is to start over", async () => {
  api({ "GET /v1/runs/{id}": run({ status: "failed", failure_reason: "refusal" }) });
  const { onStartOver } = page();
  const alert = await screen.findByRole("alert");
  expect(within(alert).queryByRole("button", { name: "Try again" })).toBeNull();
  fireEvent.click(within(alert).getByRole("button", { name: "Start over" }));
  expect(onStartOver).toHaveBeenCalled();
});

test("a change that failed leaves the video on screen and says so above it", async () => {
  // The video was made by an earlier edit; the one that failed came after it.
  withVideo({
    "GET /v1/projects/{id}/timelines": { items: [versionSummary(1, { author_id: runId(9) })], next_before: null },
    "GET /v1/runs/{id}": run({ status: "failed", failure_reason: "budget", created_at: "2026-10-17T12:00:00Z" }),
  });
  page();
  expect(await screen.findByRole("heading", { name: "Here's your video" })).toBeTruthy();
  const alert = screen.getByRole("alert");
  expect(alert.textContent).toContain("I couldn't make that change.");
  expect(alert.textContent).toContain("your video is as it was");
  expect(screen.getByRole("button", { name: "Looks great" })).toBeTruthy();
});

test("what a failed edit left behind is not shown as the video: the first video failed, so there is none yet", async () => {
  const fake = withVideo({ "GET /v1/runs/{id}": run({ status: "failed", failure_reason: "budget" }) });
  page();
  const alert = await screen.findByRole("alert");
  expect(alert.textContent).toContain("I couldn't finish your video");
  expect(screen.queryByRole("heading", { name: "Here's your video" })).toBeNull();
  expect(fake.callsTo("GET /v1/projects/{id}/renders")).toHaveLength(0);
});

test("a change that failed halfway shows the video from before it, not the half-made one", async () => {
  const fake = withVideo({
    "GET /v1/projects/{id}/timelines": { items: [versionSummary(2, { author_id: R }), versionSummary(1, { author_id: runId(9) })], next_before: null },
    "GET /v1/runs/{id}": run({ status: "failed", failure_reason: "model_error", created_at: "2026-10-17T12:00:00Z" }),
  });
  page();
  expect(await screen.findByRole("heading", { name: "Here's your video" })).toBeTruthy();
  expect(screen.getByRole("alert").textContent).toContain("I couldn't make that change.");
  await waitFor(() => expect(fake.callsTo("GET /v1/projects/{id}/renders").length).toBeGreaterThan(0));
  expect(fake.callsTo("GET /v1/projects/{id}/renders").every((call) => call.query.get("version") === "1")).toBe(true);
});

test("an edit that has just finished waits for its video instead of showing the one before", async () => {
  // The edit ended on video 2; the list still only has video 1.
  withVideo({ "GET /v1/runs/{id}": run({ status: "succeeded", timeline_version: 2 }) });
  page();
  await waitFor(() => expect(screen.getByRole("status").textContent).toBe("Loading your video"));
  await new Promise((resolve) => setTimeout(resolve, 50));
  expect(screen.queryByRole("heading", { name: "Here's your video" })).toBeNull();
});

test("an edit that cannot be asked about says so and does not claim there is no video", async () => {
  const fake = api({ "GET /v1/runs/{id}": apiError(400, "bad_request") });
  page();
  const alert = await screen.findByRole("alert");
  expect(alert.textContent).toContain("I couldn't check on your video");
  expect(screen.queryByText("No video yet")).toBeNull();
  fake.on("GET /v1/runs/{id}", run({ status: "running" }));
  fireEvent.click(within(alert).getByRole("button", { name: "Try again" }));
  expect(await screen.findByRole("heading", { name: "I'm making your video" })).toBeTruthy();
});

test("starting the first video again asks for the style that was picked", async () => {
  window.localStorage.setItem(`cf:guided-vibe:${P}`, "hype");
  const fake = api({ "POST /v1/projects/{id}/edit": json(202, run({ id: runId(3) })) });
  page({ runId: undefined });
  fireEvent.click(await screen.findByRole("button", { name: "Make my video" }));
  await waitFor(() => expect(fake.callsTo("POST /v1/projects/{id}/edit")[0]?.body).toEqual({ vibe: "hype" }));
});

test("a video with no edit yet (the editor never started) offers to make it", async () => {
  const fake = api({ "POST /v1/projects/{id}/edit": json(202, run({ id: runId(3) })) });
  const { onRunStarted } = page({ runId: undefined });
  expect(await screen.findByRole("heading", { name: "No video yet" })).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "Make my video" }));
  await waitFor(() => expect(onRunStarted).toHaveBeenCalledWith(runId(3)));
  expect(fake.callsTo("POST /v1/projects/{id}/edit")).toHaveLength(1);
});

test("a start the API refuses on that screen is said in a sentence", async () => {
  api({ "POST /v1/projects/{id}/edit": apiError(409, "run_in_progress") });
  page({ runId: undefined });
  fireEvent.click(await screen.findByRole("button", { name: "Make my video" }));
  expect(await screen.findByText("I'm still working on this video. Wait until I'm done, or stop me first.")).toBeTruthy();
});

test("a video that is gone says so, and the one step is a new video", async () => {
  api({ "GET /v1/projects/{id}": apiError(404, "not_found") });
  const { onStartOver } = page();
  const alert = await screen.findByRole("alert");
  expect(alert.textContent).toContain("We couldn't find this video");
  fireEvent.click(within(alert).getByRole("button", { name: "Make a new video" }));
  expect(onStartOver).toHaveBeenCalled();
});

test("a video that cannot be loaded says so, and the one step is to try again", async () => {
  const fake = api({ "GET /v1/projects/{id}": apiError(400, "bad_request"), "GET /v1/runs/{id}": run({ status: "running" }) });
  page();
  const alert = await screen.findByRole("alert");
  expect(alert.textContent).toContain("This video could not be loaded");
  fake.on("GET /v1/projects/{id}", project(1));
  fireEvent.click(within(alert).getByRole("button", { name: "Try again" }));
  expect(await screen.findByRole("heading", { name: "I'm making your video" })).toBeTruthy();
});
