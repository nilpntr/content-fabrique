import { cleanup, fireEvent, screen, waitFor, within } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { createMemoryCommentsStore, type CommentsStore } from "@/components/project/comments-store";
import { rememberRun } from "@/components/project/last-run";
import { apiError, type FakeApi, installApi, json, renderApp } from "@/test-api";
import { clip, event, project, projectId, readyRender, render, renderId, run, runId, timelineVersion, versionSummary } from "@/test-fixtures";
import { ProjectPage } from "./project";

vi.mock("@tanstack/react-router", async (original) => ({
  ...(await original<typeof import("@tanstack/react-router")>()),
  Link: ({ to, children, className }: { to: string; children: ReactNode; className?: string }) => (
    <a href={to} className={className}>
      {children}
    </a>
  ),
}));

// The run is polled every 1.5 s and a render every 2 s; the tests that wait for a poll take a few real seconds.
const POLL = { timeout: 6000 };

beforeEach(() => {
  vi.spyOn(HTMLMediaElement.prototype, "play").mockResolvedValue(undefined);
  vi.spyOn(HTMLMediaElement.prototype, "pause").mockImplementation(() => {});
  window.localStorage.clear();
});
afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

const P = projectId(1);
// v1: Row 1 then Row 2. v2: Row 2 trimmed, Row 1 dropped. v3: Row 2 sped up.
const SPECS = [
  timelineVersion(1, [clip("c1", 1, 0, 0, 4000), clip("c2", 2, 4000, 0, 4000)], { summary: ["Added c1", "Added c2"] }),
  timelineVersion(2, [clip("c2", 2, 0, 500, 4000)], { summary: ["Removed c1", "Trimmed c2 by 500 ms at the start"] }),
  timelineVersion(3, [clip("c2", 2, 0, 500, 4000, 2)], { summary: ["Sped c2 up to 2x"] }),
];
const SUMMARIES = [3, 2, 1].map((n) => versionSummary(n, { summary: SPECS[n - 1]?.summary, author_id: runId(1) }));

/** The API of a project with three versions and a finished run. Renders are answered by the test. */
function api(extra: Record<string, object> = {}): FakeApi {
  return installApi({
    "GET /v1/projects/{id}": project(1, { current_timeline_version: 3, current_version: SUMMARIES[0] }),
    "GET /v1/projects/{id}/timelines": { items: SUMMARIES, next_before: null },
    "GET /v1/projects/{id}/timelines/{version}": (call: { path: string }) => SPECS[Number(call.path.split("/").at(-1)) - 1] as object,
    "GET /v1/runs/{id}": run({ status: "succeeded", summary: "Cut it down to the row.", timeline_version: 3 }),
    ...extra,
  });
}

function page(props: Partial<Parameters<typeof ProjectPage>[0]> = {}, comments?: CommentsStore) {
  return renderApp(<ProjectPage projectId={P} onRunStarted={vi.fn()} comments={comments ?? createMemoryCommentsStore()} {...props} />);
}

/** The review screen is up: the versions and their specs have loaded. */
const reviewUp = () => screen.findByRole("group", { name: "Version" });
const selectorButton = (version: number) => within(screen.getByRole("group", { name: "Version" })).getByRole("button", { name: new RegExp(`^v${version}`) });
const noRenders = { "GET /v1/projects/{id}/renders": apiError(404, "not_found") };

test("the review shows the latest version: its clip names from the project's footage, and the versions with their change summaries", async () => {
  api(noRenders);
  page();
  await reviewUp();

  expect(screen.getByRole("heading", { level: 1 }).textContent).toBe("Project 1");
  expect(selectorButton(3).getAttribute("aria-pressed")).toBe("true");
  // The filmstrip labels the clip with the asset's title.
  expect(within(screen.getByRole("region", { name: "Timeline" })).getByText("Row 2")).toBeTruthy();
  const history = screen.getByRole("region", { name: "Versions" });
  expect(within(history).getByText("Sped c2 up to 2x")).toBeTruthy();
  expect(within(history).getByText("Removed c1")).toBeTruthy();
  expect(within(history).getByText("Added c2")).toBeTruthy();
  expect(within(history).getAllByRole("button")).toHaveLength(3);
});

test("with no renders API there is no video, a placeholder that says so, and a notice; the timeline still works", async () => {
  api(noRenders);
  page();
  await reviewUp();
  expect(await screen.findByText(/Video previews aren't available yet/)).toBeTruthy();
  expect(document.querySelector("video")).toBeNull();
  expect(screen.getByText("No video preview yet")).toBeTruthy();
  expect(screen.queryByRole("button", { name: "Render preview" })).toBeNull();
  expect(screen.getByRole("group", { name: /Timeline tracks/ })).toBeTruthy();
});

test("a ready preview plays the render's MP4 and uses its time map", async () => {
  const fake = api({
    "GET /v1/projects/{id}/renders": { items: [render({ id: renderId(1), timeline_version: 3, status: "ready" })], next_cursor: null },
    "GET /v1/renders/{id}": readyRender({ id: renderId(1), timeline_version: 3, duration_ms: 1750 }),
    "GET /time-map.json": [{ out_start_ms: 0, out_end_ms: 1750, item_id: "c2", asset_id: "ast_x", source_in_ms: 500, speed: 2 }],
  });
  page();
  await waitFor(() => expect(document.querySelector("video")?.getAttribute("src")).toBe("https://files.test/preview.mp4"));
  expect(screen.queryByTestId("placeholder-frame")).toBeNull();
  expect(fake.callsTo("GET /v1/projects/{id}/renders")[0]?.query.get("version")).toBe("3");
  expect(fake.callsTo("GET /v1/projects/{id}/renders")[0]?.query.get("profile")).toBe("preview");
  await waitFor(() => expect(fake.callsTo("GET /time-map.json")).toHaveLength(1));
  // No notice or button: the video is the answer.
  expect(screen.queryByRole("button", { name: "Render preview" })).toBeNull();
  expect(screen.queryByText(/aren't available/)).toBeNull();
});

test("a time map that cannot be fetched falls back to the one made from the spec; the video still plays", async () => {
  const fake = api({
    "GET /v1/projects/{id}/renders": { items: [render({ timeline_version: 3, status: "ready" })], next_cursor: null },
    "GET /v1/renders/{id}": readyRender({ timeline_version: 3 }),
    "GET /time-map.json": apiError(403, "forbidden"),
  });
  page();
  await waitFor(() => expect(document.querySelector("video")?.getAttribute("src")).toBe("https://files.test/preview.mp4"));
  await waitFor(() => expect(fake.callsTo("GET /time-map.json")).toHaveLength(1));
  expect(screen.getByRole("group", { name: /Timeline tracks/ })).toBeTruthy();
});

test("without a preview, 'Render preview' asks for one; its progress shows, and when it is ready the video appears", async () => {
  let polls = 0;
  let queued = false;
  const fake = api({
    "GET /v1/projects/{id}/renders": () => ({ items: queued ? [render({ id: renderId(2), timeline_version: 3, status: "queued" })] : [], next_cursor: null }),
    "POST /v1/projects/{id}/renders": () => {
      queued = true;
      return json(202, render({ id: renderId(2), timeline_version: 3, status: "queued" }));
    },
    "GET /v1/renders/{id}": () => (polls++ === 0 ? render({ id: renderId(2), timeline_version: 3, status: "rendering", progress: 40, stage: "rendering" }) : readyRender({ id: renderId(2), timeline_version: 3 })),
  });
  page();
  expect(await screen.findByText("There is no video preview of v3 yet.")).toBeTruthy();

  fireEvent.click(screen.getByRole("button", { name: "Render preview" }));
  const bar = await screen.findByRole("progressbar", { name: "Preview of v3" });
  expect(fake.callsTo("POST /v1/projects/{id}/renders")[0]?.body).toEqual({ version: 3, profile: "preview" });
  // The queued render first, then what the render says when it is fetched.
  expect(bar.getAttribute("aria-valuenow")).toBe("0");
  await waitFor(() => expect(screen.getByRole("progressbar", { name: "Preview of v3" }).getAttribute("aria-valuenow")).toBe("40"));
  expect(screen.getByText(/Rendering a preview of v3/)).toBeTruthy();

  await waitFor(() => expect(document.querySelector("video")?.getAttribute("src")).toBe("https://files.test/preview.mp4"), POLL);
  expect(screen.queryByRole("progressbar")).toBeNull();
}, 12_000);

test("a failed preview says why and can be tried again; a refused request says why", async () => {
  const fake = api({
    "GET /v1/projects/{id}/renders": { items: [render({ timeline_version: 3, status: "failed", failure_reason: "source_unavailable" })], next_cursor: null },
    "GET /v1/renders/{id}": render({ timeline_version: 3, status: "failed", failure_reason: "source_unavailable" }),
    "POST /v1/projects/{id}/renders": apiError(422, "timeline_invalid"),
  });
  page();
  expect((await screen.findByRole("alert")).textContent).toBe("The preview of v3 could not be made: a clip it uses is gone.");
  fireEvent.click(screen.getByRole("button", { name: "Try again" }));
  await waitFor(() => expect(screen.getAllByRole("alert").map((alert) => alert.textContent).join(" ")).toMatch(/a clip it uses has been deleted/));
  expect(fake.callsTo("POST /v1/projects/{id}/renders")).toHaveLength(1);
});

test("a video that will not load (its URL expired) asks for fresh URLs", async () => {
  const fake = api({
    "GET /v1/projects/{id}/renders": { items: [render({ timeline_version: 3, status: "ready" })], next_cursor: null },
    "GET /v1/renders/{id}": readyRender({ timeline_version: 3 }),
    "GET /time-map.json": [],
  });
  page();
  await waitFor(() => expect(document.querySelector("video")).toBeTruthy());
  const before = fake.callsTo("GET /v1/renders/{id}").length;
  fireEvent.error(document.querySelector("video") as HTMLVideoElement);
  await waitFor(() => expect(fake.callsTo("GET /v1/renders/{id}").length).toBe(before + 1));
});

test("choosing another version shows it, and asks for that version's preview", async () => {
  const fake = api({ "GET /v1/projects/{id}/renders": { items: [], next_cursor: null } });
  page();
  expect(await screen.findByText("There is no video preview of v3 yet.")).toBeTruthy();

  fireEvent.click(within(screen.getByRole("region", { name: "Versions" })).getByRole("button", { name: /^v1/ }));
  expect(await screen.findByText("There is no video preview of v1 yet.")).toBeTruthy();
  expect(selectorButton(1).getAttribute("aria-pressed")).toBe("true");
  expect(screen.getByText(/You are looking at v1, not the latest version/)).toBeTruthy();
  expect(fake.callsTo("GET /v1/projects/{id}/renders").map((call) => call.query.get("version"))).toEqual(["3", "1"]);
  // v1 has two clips: both are on its filmstrip.
  const timeline = within(screen.getByRole("region", { name: "Timeline" }));
  expect(timeline.getByText("Row 1")).toBeTruthy();
  expect(timeline.getByText("Row 2")).toBeTruthy();
  // The selector takes it back.
  fireEvent.click(selectorButton(3));
  expect(await screen.findByText("There is no video preview of v3 yet.")).toBeTruthy();
  expect(selectorButton(3).getAttribute("aria-pressed")).toBe("true");
});

test("comments are kept in the store, labelled as not saved, and come back after the screen reopens", async () => {
  api(noRenders);
  const store = createMemoryCommentsStore();
  const first = page({}, store);
  await reviewUp();
  const panel = screen.getByRole("complementary", { name: "Comment history" });
  expect(within(panel).getByText(/Comments aren't saved yet/)).toBeTruthy();

  fireEvent.keyDown(window, { key: "c" });
  fireEvent.change(screen.getByRole("textbox", { name: "Comment" }), { target: { value: "Tighten this" } });
  fireEvent.click(screen.getByRole("button", { name: /Save comment/ }));
  expect(within(panel).getByText("Tighten this")).toBeTruthy();
  expect(store.list(P)).toHaveLength(1);
  expect(store.list(P)[0]).toMatchObject({ body: "Tighten this", version: 3 });

  first.unmount();
  page({}, store);
  await reviewUp();
  expect(within(screen.getByRole("complementary", { name: "Comment history" })).getByText("Tighten this")).toBeTruthy();
});

test("edit again starts a run with the new instructions and reports it; the spending cap is shown", async () => {
  const fake = api({ ...noRenders, "POST /v1/projects/{id}/edit": json(202, run({ id: runId(9), status: "queued" })) });
  const onRunStarted = vi.fn();
  page({ onRunStarted });
  await reviewUp();
  fireEvent.click(screen.getByRole("button", { name: "Edit again" }));
  expect(screen.getByText("$5.00")).toBeTruthy();
  fireEvent.change(screen.getByRole("textbox", { name: /What should change/ }), { target: { value: " Make it shorter " } });
  fireEvent.click(within(screen.getByRole("form", { name: "Edit again" })).getByRole("button", { name: "Edit again" }));
  await waitFor(() => expect(onRunStarted).toHaveBeenCalledWith(runId(9)));
  expect(fake.callsTo("POST /v1/projects/{id}/edit")[0]?.body).toEqual({ instructions: "Make it shorter" });
  expect(fake.callsTo("POST /v1/projects/{id}/edit")[0]?.path).toBe(`/v1/projects/${P}/edit`);
  // The form closes.
  expect(screen.queryByRole("form", { name: "Edit again" })).toBeNull();
});

test.each([
  [409, "run_in_progress", /still working/],
  [422, "assets_not_usable", /no ready video clip/],
  [402, "plan_required", /active plan/],
  [403, "insufficient_role", /can't start the editor/],
])("edit again refused with %s %s says why and keeps the instructions", async (status, code, message) => {
  api({ ...noRenders, "POST /v1/projects/{id}/edit": apiError(status, code) });
  const onRunStarted = vi.fn();
  page({ onRunStarted });
  await reviewUp();
  fireEvent.click(screen.getByRole("button", { name: "Edit again" }));
  fireEvent.change(screen.getByRole("textbox", { name: /What should change/ }), { target: { value: "Shorter" } });
  fireEvent.click(within(screen.getByRole("form", { name: "Edit again" })).getByRole("button", { name: "Edit again" }));
  expect((await screen.findByRole("alert")).textContent).toMatch(message);
  expect((screen.getByRole("textbox", { name: /What should change/ }) as HTMLTextAreaElement).value).toBe("Shorter");
  expect(onRunStarted).not.toHaveBeenCalled();
});

test("while a run is going the panel follows it and Edit again waits; when it finishes the new version appears", async () => {
  let runPolls = 0;
  let finished = false;
  const fake = api({
    ...noRenders,
    "GET /v1/projects/{id}/timelines": () => ({ items: finished ? SUMMARIES : SUMMARIES.slice(1), next_before: null }),
    "GET /v1/runs/{id}": () => {
      if (runPolls++ < 1) return run({ id: runId(5), status: "running", active_ms: 3000, events: [event(1, "tool_call", 'edit_timeline {"base_version":1,"ops":[', { tool: "edit_timeline" })], last_seq: 1 });
      finished = true;
      return run({ id: runId(5), status: "succeeded", summary: "Sped the row up.", timeline_version: 3 });
    },
  });
  page({ runId: runId(5) });
  await reviewUp();
  const panel = within(await screen.findByRole("region", { name: "The editor is working" }));
  expect(panel.getByRole("status").textContent).toBe("Editing the timeline (v2)…");
  expect(selectorButton(2).getAttribute("aria-pressed")).toBe("true");
  expect(screen.getByRole("button", { name: "Edit again" }).hasAttribute("disabled")).toBe(true);
  expect(screen.getByText(/You can edit again when it has finished/)).toBeTruthy();

  // The next poll: the run is done, so the project and its versions are fetched again and the screen reopens on v3.
  expect(await screen.findByRole("heading", { name: "The editor finished" }, POLL)).toBeTruthy();
  await waitFor(() => expect(selectorButton(3).getAttribute("aria-pressed")).toBe("true"));
  expect(fake.callsTo("GET /v1/projects/{id}/timelines").length).toBeGreaterThan(1);
  expect(screen.getByRole("button", { name: "Edit again" }).hasAttribute("disabled")).toBe(false);
}, 12_000);

test("a project opened without ?run= follows the run this browser started, else the run that made the latest version", async () => {
  const fake = api(noRenders);
  const first = page();
  await reviewUp();
  expect(fake.callsTo("GET /v1/runs/{id}")[0]?.path).toBe(`/v1/runs/${runId(1)}`);
  expect(await screen.findByRole("heading", { name: "The editor finished" })).toBeTruthy();
  first.unmount();

  rememberRun(P, runId(4));
  page();
  await reviewUp();
  await waitFor(() => expect(fake.callsTo("GET /v1/runs/{id}").at(-1)?.path).toBe(`/v1/runs/${runId(4)}`));
});

test("before the first cut: a run that is going says it is on its way; with none, the editor can be started", async () => {
  const empty = { "GET /v1/projects/{id}": project(1), "GET /v1/projects/{id}/timelines": { items: [], next_before: null } };
  api({ ...empty, "GET /v1/runs/{id}": run({ status: "running" }) });
  page({ runId: runId(1) });
  expect(await screen.findByRole("heading", { name: "Your first cut is on its way" })).toBeTruthy();
  cleanup();

  api({ ...empty });
  page();
  expect(await screen.findByRole("heading", { name: "No cut yet" })).toBeTruthy();
  expect(screen.getByRole("button", { name: "Start the editor" })).toBeTruthy();
  expect(screen.queryByText("Waiting for the editor to start…")).toBeNull();
});

test("a project that does not exist, or cannot be loaded, says so", async () => {
  const fake = installApi({ "GET /v1/projects/{id}": apiError(404, "not_found") });
  page();
  expect((await screen.findByRole("alert")).textContent).toContain("We couldn't find this project");
  expect(screen.getByRole("link", { name: /All projects/ }).getAttribute("href")).toBe("/projects");
  cleanup();

  fake.on("GET /v1/projects/{id}", apiError(403, "no_active_workspace"));
  page();
  expect((await screen.findByRole("alert")).textContent).toContain("could not be loaded");
});

test("the header shows the format, the status, the clip count, and the footage lists a clip that is gone", async () => {
  api({
    ...noRenders,
    "GET /v1/projects/{id}": project(1, {
      status: "editing",
      asset_count: 1,
      assets: [
        { asset_id: "ast_01jh6m00000000000000000001", position: 0, title: "Row 1", kind: "video", status: "ready", duration_ms: 1000, available: true },
        { asset_id: "ast_01jh6m00000000000000000002", position: 1, title: "Row 2", kind: "video", status: "ready", duration_ms: 1000, available: false },
      ],
    }),
  });
  page();
  await reviewUp();
  const header = screen.getByRole("heading", { level: 1 }).parentElement as HTMLElement;
  expect(within(header).getByText("9:16")).toBeTruthy();
  expect(within(header).getByText("Editing")).toBeTruthy();
  expect(within(header).getByText("1 clip")).toBeTruthy();
  fireEvent.click(screen.getByText("Brief and footage"));
  expect(screen.getByText("Open on the pose")).toBeTruthy();
  expect(screen.getByText("No longer available")).toBeTruthy();
});
