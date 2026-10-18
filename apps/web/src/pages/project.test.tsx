import { act, cleanup, fireEvent, screen, waitFor, within } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { rememberRun } from "@/components/project/last-run";
import { apiError, type FakeApi, installApi, json, renderApp } from "@/test-api";
import { ME } from "@/test-clerk";
import { annotation, annotationId, clip, event, project, projectId, readyRender, render, renderId, run, runId, timelineVersion, v2Artifacts, v2Run, versionSummary } from "@/test-fixtures";
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
    "GET /v1/me": ME,
    "GET /v1/annotations": { items: [], next_cursor: null },
    "GET /v1/projects/{id}": project(1, { current_timeline_version: 3, current_version: SUMMARIES[0] }),
    "GET /v1/projects/{id}/timelines": { items: SUMMARIES, next_before: null },
    "GET /v1/projects/{id}/timelines/{version}": (call: { path: string }) => SPECS[Number(call.path.split("/").at(-1)) - 1] as object,
    "GET /v1/runs/{id}": run({ status: "succeeded", summary: "Cut it down to the row.", timeline_version: 3 }),
    ...extra,
  });
}

function page(props: Partial<Parameters<typeof ProjectPage>[0]> = {}) {
  return renderApp(<ProjectPage projectId={P} onRunStarted={vi.fn()} {...props} />);
}

/** The review screen is up: the versions and their specs have loaded. */
const reviewUp = () => screen.findByRole("group", { name: "Version" });
const selectorButton = (version: number) => within(screen.getByRole("group", { name: "Version" })).getByRole("button", { name: `Version ${version}` });
const noRenders = { "GET /v1/projects/{id}/renders": apiError(404, "not_found") };
/** The renders asked for to play a version (the download asks for its own, `profile=final`). */
const previewCalls = (fake: FakeApi) => fake.callsTo("GET /v1/projects/{id}/renders").filter((call) => call.query.get("profile") === "preview");
const details = () => screen.getByTestId("project-details") as HTMLDetailsElement;
/** Opens "Details", as a click on its summary does. */
const openDetails = () =>
  act(() => {
    details().open = true;
    details().dispatchEvent(new Event("toggle"));
  });

test("the review shows the latest version: its clip names from the project's footage, and (in Details) the versions with their change summaries", async () => {
  api(noRenders);
  page();
  await reviewUp();

  expect(screen.getByRole("heading", { level: 1 }).textContent).toBe("Project 1");
  expect(selectorButton(3).getAttribute("aria-pressed")).toBe("true");
  // The filmstrip labels the clip with the asset's title.
  expect(within(screen.getByRole("region", { name: "Timeline" })).getByText("Row 2")).toBeTruthy();
  // What changed in the version on screen names the clip, not its id.
  expect(screen.getByText("Sped Row 2 up to 2x")).toBeTruthy();
  expect(screen.queryByRole("region", { name: "Versions" })).toBeNull();
  openDetails();
  const history = await screen.findByRole("region", { name: "Versions" });
  expect(within(history).getByText("Sped c2 up to 2x")).toBeTruthy();
  expect(within(history).getByText("Removed c1")).toBeTruthy();
  expect(within(history).getByText("Added c2")).toBeTruthy();
  expect(within(history).getAllByRole("button")).toHaveLength(3);
});

test("with no renders API there is no video, a placeholder that says so, and a notice; the timeline still works", async () => {
  api(noRenders);
  page();
  await reviewUp();
  expect(await screen.findByText(/Videos can't be played here yet/)).toBeTruthy();
  expect(document.querySelector("video")).toBeNull();
  expect(screen.getByText("No video yet")).toBeTruthy();
  expect(screen.queryByRole("button", { name: "Show me" })).toBeNull();
  // No renders, so nothing to download either.
  expect(screen.queryByTestId("download")).toBeNull();
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
  expect(previewCalls(fake)[0]?.query.get("version")).toBe("3");
  await waitFor(() => expect(fake.callsTo("GET /time-map.json")).toHaveLength(1));
  // No notice or button: the video is the answer.
  expect(screen.queryByRole("button", { name: "Show me" })).toBeNull();
  expect(screen.queryByText(/can't be played here/)).toBeNull();
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

test("without a preview, 'Show me' asks for one; its progress shows, and when it is ready the video appears", async () => {
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
  expect(await screen.findByText("Version 3 isn't ready to watch yet.")).toBeTruthy();

  fireEvent.click(screen.getByRole("button", { name: "Show me" }));
  const bar = await screen.findByRole("progressbar", { name: "Preparing Version 3" });
  expect(fake.callsTo("POST /v1/projects/{id}/renders")[0]?.body).toEqual({ version: 3, profile: "preview" });
  // The queued render first, then what the render says when it is fetched.
  expect(bar.getAttribute("aria-valuenow")).toBe("0");
  await waitFor(() => expect(screen.getByRole("progressbar", { name: "Preparing Version 3" }).getAttribute("aria-valuenow")).toBe("40"));
  expect(screen.getByText(/Putting your video together/)).toBeTruthy();

  await waitFor(() => expect(document.querySelector("video")?.getAttribute("src")).toBe("https://files.test/preview.mp4"), POLL);
  expect(screen.queryByRole("progressbar", { name: "Preparing Version 3" })).toBeNull();
}, 12_000);

test("a failed preview says why and can be tried again; a refused request says why", async () => {
  const fake = api({
    "GET /v1/projects/{id}/renders": { items: [render({ timeline_version: 3, status: "failed", failure_reason: "source_unavailable" })], next_cursor: null },
    "GET /v1/renders/{id}": render({ timeline_version: 3, status: "failed", failure_reason: "source_unavailable" }),
    "POST /v1/projects/{id}/renders": apiError(422, "timeline_invalid"),
  });
  page();
  expect((await screen.findByRole("alert")).textContent).toBe("Version 3 could not be shown: a clip it uses has been deleted.");
  fireEvent.click(screen.getByRole("button", { name: "Try again" }));
  await waitFor(() => expect(screen.getAllByRole("alert").map((alert) => alert.textContent).join(" ")).toMatch(/uses a clip that has been deleted/));
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
  expect(await screen.findByText("Version 3 isn't ready to watch yet.")).toBeTruthy();

  fireEvent.click(selectorButton(1));
  expect(await screen.findByText("Version 1 isn't ready to watch yet.")).toBeTruthy();
  expect(selectorButton(1).getAttribute("aria-pressed")).toBe("true");
  expect(screen.getByText(/You're watching Version 1, not the latest/)).toBeTruthy();
  expect(previewCalls(fake).map((call) => call.query.get("version"))).toEqual(["3", "1"]);
  // The download follows the version on screen.
  expect(screen.getByRole("button", { name: "Download Version 1" })).toBeTruthy();
  // v1 has two clips: both are on its filmstrip.
  const timeline = within(screen.getByRole("region", { name: "Timeline" }));
  expect(timeline.getByText("Row 1")).toBeTruthy();
  expect(timeline.getByText("Row 2")).toBeTruthy();
  // The selector takes it back.
  fireEvent.click(selectorButton(3));
  expect(await screen.findByText("Version 3 isn't ready to watch yet.")).toBeTruthy();
  expect(selectorButton(3).getAttribute("aria-pressed")).toBe("true");
});

// ── Comments (P1-08) ────────────────────────────────────────────────────────

const panel = () => screen.getByRole("complementary", { name: "Comment history" });
const noMs = (n: number) => n;

/** A stateful /v1/annotations: what was written is what the next list returns. */
function annotationsApi(initial: ReturnType<typeof annotation>[] = [], extra: Record<string, object> = {}) {
  const stored = [...initial];
  const fake = api({
    ...noRenders,
    "GET /v1/annotations": () => ({ items: stored, next_cursor: null }),
    "POST /v1/annotations": (call: { body: unknown }) => {
      const body = call.body as { target_type: "render" | "timeline_version"; target_id: string; start_ms: number; end_ms: number; body: string; pin?: { x: number; y: number } };
      const made = annotation(stored.length + 1, {
        id: annotationId(100 + stored.length),
        target_type: body.target_type,
        target_id: body.target_id,
        start_ms: body.start_ms,
        end_ms: body.end_ms,
        body: body.body,
        pin: body.pin ?? null,
      });
      stored.push(made);
      return json(201, made);
    },
    "PATCH /v1/annotations/{id}": (call: { path: string; body: unknown }) => {
      const i = stored.findIndex((a) => call.path.endsWith(a.id));
      stored[i] = { ...(stored[i] as (typeof stored)[number]), ...(call.body as object), status: ((call.body as { status?: string }).status ?? stored[i]?.status) as "open" } as (typeof stored)[number];
      return stored[i] as object;
    },
    "DELETE /v1/annotations/{id}": (call: { path: string }) => {
      const i = stored.findIndex((a) => call.path.endsWith(a.id));
      stored.splice(i, 1);
      return json(204, null);
    },
    ...extra,
  });
  return { fake, stored };
}

const saveComment = (body: string) => {
  fireEvent.keyDown(window, { key: "c" });
  fireEvent.change(screen.getByRole("textbox", { name: "Comment" }), { target: { value: body } });
  fireEvent.click(screen.getByRole("button", { name: /Save comment/ }));
};

test("the project's comments come from the API: yours, a teammate's and the editor's reply, with their status and the version they were made on", async () => {
  const { fake } = annotationsApi([
    annotation(1, { body: "Tighten this", pin: { x: 0.25, y: 0.5 } }),
    annotation(2, { body: "Use the other take", author: { kind: "user", id: "usr_01other" }, timeline_version: 1 }),
    annotation(3, { body: "Done: trimmed.", parent_id: annotationId(1), author: { kind: "agent", id: runId(1) }, status: "open" }),
    annotation(4, { body: "Make the title bigger", status: "applied", addressed_in_version: 3, timeline_version: 2, author: { kind: "user", id: "usr_01test" } }),
  ]);
  page();
  await reviewUp();
  await waitFor(() => expect(within(panel()).getByText("Tighten this")).toBeTruthy());
  expect(fake.callsTo("GET /v1/annotations")[0]?.query.get("project_id")).toBe(P);
  const row = (text: string) => within(panel()).getByText(text).closest("li") as HTMLElement;
  expect(within(row("Tighten this")).getByText("You")).toBeTruthy();
  expect(within(row("Use the other take")).getByText("Teammate")).toBeTruthy();
  expect(within(row("Done: trimmed.")).getByText("Editor")).toBeTruthy();
  expect(within(row("Done: trimmed.")).getByText("Agent reply")).toBeTruthy();
  expect(within(row("Make the title bigger")).getByText("on Version 2")).toBeTruthy();
  expect(within(row("Make the title bigger")).getAllByText(/Addressed in Version 3|Changed in Version 3/).length).toBeGreaterThan(0);
  expect(within(panel()).queryByText(/aren't saved/)).toBeNull();
});

test("a comment is written to the API with its version as the target, shows at once, and is still there after the page reopens", async () => {
  let release: (() => void) | undefined;
  const gate = new Promise<void>((resolve) => {
    release = resolve;
  });
  const { fake, stored } = annotationsApi([], {});
  const post = fake.callsTo;
  const first = page();
  await reviewUp();
  fake.on("POST /v1/annotations", async (call: { body: unknown }) => {
    await gate;
    const body = call.body as { target_type: string; target_id: string; start_ms: number; end_ms: number; body: string };
    const made = annotation(1, { id: annotationId(100), target_id: body.target_id, start_ms: body.start_ms, end_ms: body.end_ms, body: body.body });
    stored.push(made);
    return json(201, made);
  });

  saveComment("Tighten this");
  // Shown before the API has answered.
  expect(within(panel()).getByText("Tighten this")).toBeTruthy();
  await waitFor(() => expect(post("POST /v1/annotations")).toHaveLength(1));
  expect(post("POST /v1/annotations")[0]?.body).toEqual({
    target_type: "timeline_version",
    target_id: SUMMARIES[0]?.id,
    project_id: P,
    start_ms: noMs(0),
    end_ms: noMs(0),
    body: "Tighten this",
  });
  release?.();
  await waitFor(() => expect(within(panel()).getByText("Tighten this").closest("li")?.getAttribute("data-comment-id")).toBe(annotationId(100)));
  // The placeholder was swapped for the saved comment, which stays selected.
  expect(within(panel()).getAllByRole("listitem")).toHaveLength(1);
  expect(within(panel()).getByText("Tighten this").closest("li")?.getAttribute("data-selected")).toBe("true");

  first.unmount();
  page();
  await reviewUp();
  expect(await within(panel()).findByText("Tighten this")).toBeTruthy();
});

test("with the preview of the version on screen ready, the comment is about the render", async () => {
  const { fake } = annotationsApi([], {
    "GET /v1/projects/{id}/renders": { items: [render({ id: renderId(1), timeline_version: 3, status: "ready" })], next_cursor: null },
    "GET /v1/renders/{id}": readyRender({ id: renderId(1), timeline_version: 3 }),
    "GET /time-map.json": [],
  });
  page();
  await waitFor(() => expect(document.querySelector("video")?.getAttribute("src")).toBe("https://files.test/preview.mp4"));
  saveComment("On the video");
  await waitFor(() => expect(fake.callsTo("POST /v1/annotations")).toHaveLength(1));
  expect(fake.callsTo("POST /v1/annotations")[0]?.body).toMatchObject({ target_type: "render", target_id: renderId(1), body: "On the video" });
});

test("a comment the API refuses is taken back, with the reason; the others stay", async () => {
  const { fake } = annotationsApi([annotation(1, { body: "Already saved" })], { "POST /v1/annotations": apiError(403, "insufficient_role") });
  page();
  await reviewUp();
  await within(panel()).findByText("Already saved");
  saveComment("Will be refused");
  await waitFor(() => expect(fake.callsTo("POST /v1/annotations")).toHaveLength(1));
  await waitFor(() => expect(within(panel()).queryByText("Will be refused")).toBeNull());
  expect(within(panel()).getByText("Already saved")).toBeTruthy();
  expect(within(panel()).getByRole("alert").textContent).toMatch(/You can only change your own comments/);
  // Dismissed.
  fireEvent.click(within(panel()).getByRole("button", { name: "Dismiss" }));
  expect(within(panel()).queryByRole("alert")).toBeNull();
});

test("a comment dragged past the end of the render stops at its last frame", async () => {
  const { fake } = annotationsApi([], {
    "GET /v1/projects/{id}/renders": { items: [render({ id: renderId(1), timeline_version: 3, status: "ready" })], next_cursor: null },
    "GET /v1/renders/{id}": readyRender({ id: renderId(1), timeline_version: 3, duration_ms: 1000 }),
    "GET /time-map.json": [],
  });
  page();
  await waitFor(() => expect(document.querySelector("video")?.getAttribute("src")).toBe("https://files.test/preview.mp4"));
  fireEvent.change(screen.getByRole("slider", { name: "Scrub" }), { target: { value: "3000" } });
  saveComment("Past the end");
  await waitFor(() => expect(fake.callsTo("POST /v1/annotations")).toHaveLength(1));
  expect(fake.callsTo("POST /v1/annotations")[0]?.body).toMatchObject({ target_type: "render", start_ms: 1000, end_ms: 1000 });
});

test("a comment whose refetch overtook it is put back when it is saved, and a delete is not undone by a refetch that started before it", async () => {
  let release: (() => void) | undefined;
  const gate = new Promise<void>((resolve) => {
    release = resolve;
  });
  const { fake, stored } = annotationsApi([annotation(7, { body: "Old one" })]);
  const { queryClient } = page();
  await reviewUp();
  await within(panel()).findByText("Old one");
  fake.on("POST /v1/annotations", async (call: { body: unknown }) => {
    await gate;
    const body = call.body as { target_id: string; start_ms: number; end_ms: number; body: string };
    const made = annotation(1, { id: annotationId(100), target_id: body.target_id, start_ms: body.start_ms, end_ms: body.end_ms, body: body.body });
    stored.push(made);
    return json(201, made);
  });
  saveComment("Raced");
  await waitFor(() => expect(fake.callsTo("POST /v1/annotations")).toHaveLength(1));
  // The list is fetched again before the POST has answered: the server does not have the comment yet.
  fake.on("GET /v1/annotations", () => ({ items: stored.filter((a) => a.id !== annotationId(100)), next_cursor: null }));
  await queryClient.invalidateQueries({ queryKey: ["annotations"] });
  release?.();
  await waitFor(() => expect(within(panel()).getByText("Raced").closest("li")?.getAttribute("data-comment-id")).toBe(annotationId(100)));
  expect(within(panel()).getAllByRole("listitem")).toHaveLength(2);
});

test("resolving and reopening a comment goes through the API; a refusal puts it back", async () => {
  const { fake } = annotationsApi([annotation(1, { body: "Tighten this" }), annotation(2, { body: "Someone else's", author: { kind: "user", id: "usr_01other" } })]);
  page();
  await reviewUp();
  const mine = async () => (await within(panel()).findByText("Tighten this")).closest("li") as HTMLElement;
  fireEvent.click(within(await mine()).getByRole("button", { name: "Resolve" }));
  await waitFor(() => expect(within(panel()).getByText("Tighten this").closest("li")?.getAttribute("data-status")).toBe("resolved"));
  expect(fake.callsTo("PATCH /v1/annotations/{id}")[0]?.body).toEqual({ status: "resolved" });
  expect(fake.callsTo("PATCH /v1/annotations/{id}")[0]?.path).toBe(`/v1/annotations/${annotationId(1)}`);
  fireEvent.click(within(await mine()).getByRole("button", { name: "Reopen" }));
  await waitFor(() => expect(within(panel()).getByText("Tighten this").closest("li")?.getAttribute("data-status")).toBe("open"));
  expect(fake.callsTo("PATCH /v1/annotations/{id}")[1]?.body).toEqual({ status: "open" });

  // Anyone who can comment can resolve a teammate's comment, but not edit or delete it.
  const theirs = within(panel()).getByText("Someone else's").closest("li") as HTMLElement;
  expect(within(theirs).getByRole("button", { name: "Resolve" })).toBeTruthy();
  expect(within(theirs).queryByRole("button", { name: "Edit" })).toBeNull();
  expect(within(theirs).queryByRole("button", { name: "Delete" })).toBeNull();

  fake.on("PATCH /v1/annotations/{id}", apiError(403, "insufficient_role"));
  fireEvent.click(within(await mine()).getByRole("button", { name: "Resolve" }));
  await waitFor(() => expect(within(panel()).getByRole("alert")).toBeTruthy());
  expect(within(panel()).getByText("Tighten this").closest("li")?.getAttribute("data-status")).toBe("open");
});

test("the author edits the text of a comment and deletes it (asking twice); a failed delete brings it back", async () => {
  const { fake, stored } = annotationsApi([annotation(1, { body: "Tighten this" }), annotation(2, { body: "Reply", parent_id: annotationId(1), author: { kind: "agent", id: runId(1) } })]);
  page();
  await reviewUp();
  const li = async () => (await within(panel()).findByText(/Tighten this|Tighten the cut/)).closest("li") as HTMLElement;
  fireEvent.click(within(await li()).getByRole("button", { name: "Edit" }));
  const box = screen.getByRole("textbox", { name: "Edit comment text" });
  fireEvent.change(box, { target: { value: "  Tighten the cut  " } });
  fireEvent.click(within(screen.getByRole("form", { name: "Edit comment" })).getByRole("button", { name: "Save" }));
  await waitFor(() => expect(fake.callsTo("PATCH /v1/annotations/{id}")).toHaveLength(1));
  expect(fake.callsTo("PATCH /v1/annotations/{id}")[0]?.body).toEqual({ body: "Tighten the cut" });
  expect(within(panel()).getByText("Tighten the cut")).toBeTruthy();

  // A refused delete: the comment and its reply come back.
  fake.on("DELETE /v1/annotations/{id}", apiError(500, "internal_error"));
  fireEvent.click(within(await li()).getByRole("button", { name: "Delete" }));
  fireEvent.click(within(await li()).getByRole("button", { name: "Delete for good" }));
  await waitFor(() => expect(within(panel()).getByRole("alert")).toBeTruthy());
  await waitFor(() => expect(within(panel()).getAllByRole("listitem")).toHaveLength(2));

  // And a delete that works takes the reply with it.
  fake.on("DELETE /v1/annotations/{id}", (call: { path: string }) => {
    stored.splice(0, stored.length);
    return json(204, call.path ? null : null);
  });
  fireEvent.click(within(await li()).getByRole("button", { name: "Delete" }));
  fireEvent.click(within(await li()).getByRole("button", { name: "Delete for good" }));
  await waitFor(() => expect(within(panel()).queryAllByRole("listitem")).toHaveLength(0));
  expect(fake.callsTo("DELETE /v1/annotations/{id}").at(-1)?.path).toBe(`/v1/annotations/${annotationId(1)}`);
});

test("if the comments cannot be loaded the review still works and says so", async () => {
  api({ ...noRenders, "GET /v1/annotations": apiError(404, "not_found") });
  page();
  await reviewUp();
  expect(await within(panel()).findByText(/Your comments could not be loaded/)).toBeTruthy();
  expect(screen.getByRole("group", { name: /Timeline tracks/ })).toBeTruthy();
});

test("'Tell me what to change' starts a run with the creator's words and reports it; no dollars are shown", async () => {
  const fake = api({ ...noRenders, "POST /v1/projects/{id}/edit": json(202, run({ id: runId(9), status: "queued" })) });
  const onRunStarted = vi.fn();
  page({ onRunStarted });
  await reviewUp();
  const box = screen.getByRole("textbox", { name: /Tell me what to change/ }) as HTMLTextAreaElement;
  expect(screen.queryByText(/\$\d/)).toBeNull();
  fireEvent.change(box, { target: { value: " Make it shorter " } });
  fireEvent.click(within(screen.getByRole("form", { name: "Tell me what to change" })).getByRole("button", { name: "Make the changes" }));
  await waitFor(() => expect(onRunStarted).toHaveBeenCalledWith(runId(9)));
  expect(fake.callsTo("POST /v1/projects/{id}/edit")[0]?.body).toEqual({ instructions: "Make it shorter" });
  expect(fake.callsTo("POST /v1/projects/{id}/edit")[0]?.path).toBe(`/v1/projects/${P}/edit`);
  // The box is emptied for the next round.
  expect(box.value).toBe("");
});

test.each([
  [409, "run_in_progress", /still working/],
  [422, "assets_not_usable", /clips can be used any more/],
  [402, "plan_required", /active plan/],
  [403, "insufficient_role", /can't edit videos/],
])("a change refused with %s %s says why in plain words, with no code, and keeps the text", async (status, code, message) => {
  api({ ...noRenders, "POST /v1/projects/{id}/edit": apiError(status, code) });
  const onRunStarted = vi.fn();
  page({ onRunStarted });
  await reviewUp();
  fireEvent.change(screen.getByRole("textbox", { name: /Tell me what to change/ }), { target: { value: "Shorter" } });
  fireEvent.click(within(screen.getByRole("form", { name: "Tell me what to change" })).getByRole("button", { name: "Make the changes" }));
  const alert = await screen.findByRole("alert");
  expect(alert.textContent).toMatch(message);
  expect(alert.textContent).not.toContain(code);
  expect((screen.getByRole("textbox", { name: /Tell me what to change/ }) as HTMLTextAreaElement).value).toBe("Shorter");
  expect(onRunStarted).not.toHaveBeenCalled();
});

test("while a run is going a calm progress card follows it and the change box waits; when it finishes the new version appears", async () => {
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
  const progress = within(await screen.findByTestId("edit-progress"));
  // The stage in plain words: no tool, no step, no version number.
  expect(progress.getByRole("status").textContent).toBe("Cutting to the music…");
  expect(progress.getByText("Cutting to the music").closest("li")?.getAttribute("data-state")).toBe("current");
  expect(screen.queryByText(/edit_timeline|Editing the timeline/)).toBeNull();
  expect(selectorButton(2).getAttribute("aria-pressed")).toBe("true");
  expect(screen.getByRole("button", { name: "Make the changes" }).getAttribute("aria-disabled")).toBe("true");
  expect(screen.getByText(/You can ask for changes when I'm done/)).toBeTruthy();

  // The next poll: the run is done, so the project and its versions are fetched again and the screen reopens on v3, with the note.
  expect(await screen.findByText("Sped the row up.", {}, POLL)).toBeTruthy();
  await waitFor(() => expect(selectorButton(3).getAttribute("aria-pressed")).toBe("true"));
  expect(screen.queryByTestId("edit-progress")).toBeNull();
  expect(fake.callsTo("GET /v1/projects/{id}/timelines").length).toBeGreaterThan(1);
  expect(screen.getByRole("button", { name: "Make the changes" }).getAttribute("aria-disabled")).not.toBe("true");
}, 12_000);

test("a project opened without ?run= follows the run this browser started, else the run that made the latest version", async () => {
  const fake = api(noRenders);
  const first = page();
  await reviewUp();
  expect(fake.callsTo("GET /v1/runs/{id}")[0]?.path).toBe(`/v1/runs/${runId(1)}`);
  expect(within(await screen.findByTestId("what-i-made")).getByText("Cut it down to the row.")).toBeTruthy();
  first.unmount();

  rememberRun(P, runId(4));
  const second = page();
  await reviewUp();
  await waitFor(() => expect(fake.callsTo("GET /v1/runs/{id}").at(-1)?.path).toBe(`/v1/runs/${runId(4)}`));
  second.unmount();

  // A remembered run that is older than the newest version's run gives way to it.
  rememberRun(P, runId(4), "2020-01-01T00:00:00Z");
  page();
  await reviewUp();
  await waitFor(() => expect(fake.callsTo("GET /v1/runs/{id}").at(-1)?.path).toBe(`/v1/runs/${runId(1)}`));
});

test("specs are fetched lazily: the latest and the one on screen, and the screen stays up while another version loads", async () => {
  let release: () => void = () => {};
  const gate = new Promise<void>((resolve) => {
    release = resolve;
  });
  const fake = api({
    ...noRenders,
    "GET /v1/projects/{id}/timelines/{version}": async (call: { path: string }) => {
      const version = Number(call.path.split("/").at(-1));
      if (version === 1) await gate;
      return SPECS[version - 1] as object;
    },
  });
  page();
  await reviewUp();
  const fetched = () => fake.callsTo("GET /v1/projects/{id}/timelines/{version}").map((call) => call.path.split("/").at(-1));
  expect(fetched()).toEqual(["3"]);

  fireEvent.click(selectorButton(1));
  // v1 is on screen but its spec is still on the way: the screen stays, with a small loading line, and v2 is never fetched.
  expect(await screen.findByText("Loading Version 1…")).toBeTruthy();
  expect(screen.getByRole("group", { name: "Version" })).toBeTruthy();
  expect(screen.queryByText("Loading Version 3…")).toBeNull();
  expect(fetched()).toEqual(["3", "1"]);
  release();
  await waitFor(() => expect(screen.queryByText("Loading Version 1…")).toBeNull());
  expect(screen.getByText(/Videos can't be played here yet/)).toBeTruthy();
  expect(fetched()).toEqual(["3", "1"]);
});

test("asking for a preview is tracked per version: another version is not shown as starting", async () => {
  let finish: () => void = () => {};
  const slow = new Promise<void>((resolve) => {
    finish = resolve;
  });
  api({
    "GET /v1/projects/{id}/renders": { items: [], next_cursor: null },
    "POST /v1/projects/{id}/renders": async () => {
      await slow;
      return json(202, render({ id: renderId(2), timeline_version: 3, status: "queued" }));
    },
  });
  page();
  fireEvent.click(await screen.findByRole("button", { name: "Show me" }));
  expect(await screen.findByRole("button", { name: "Starting…" })).toBeTruthy();
  fireEvent.click(selectorButton(2));
  expect(await screen.findByText("Version 2 isn't ready to watch yet.")).toBeTruthy();
  expect(screen.getByRole("button", { name: "Show me" }).hasAttribute("disabled")).toBe(false);
  finish();
});

test("before the first cut: a run that is going says so calmly; with none, the first video can be started", async () => {
  const empty = { "GET /v1/projects/{id}": project(1), "GET /v1/projects/{id}/timelines": { items: [], next_before: null } };
  api({ ...empty, "GET /v1/runs/{id}": run({ status: "running" }) });
  page({ runId: runId(1) });
  expect(await screen.findByRole("heading", { name: "I'm making your video" })).toBeTruthy();
  expect(screen.getByText("Getting ready…")).toBeTruthy();
  cleanup();

  api({ ...empty });
  page();
  expect(await screen.findByRole("heading", { name: "No video yet" })).toBeTruthy();
  expect(screen.getByRole("button", { name: "Make my video" })).toBeTruthy();
  expect(screen.queryByTestId("edit-progress")).toBeNull();
});

test("a project that does not exist, or cannot be loaded, says so", async () => {
  const fake = installApi({ "GET /v1/projects/{id}": apiError(404, "not_found") });
  page();
  expect((await screen.findByRole("alert")).textContent).toContain("We couldn't find this video");
  expect(screen.getByRole("link", { name: /All projects/ }).getAttribute("href")).toBe("/projects");
  cleanup();

  fake.on("GET /v1/projects/{id}", apiError(403, "no_active_workspace"));
  page();
  expect((await screen.findByRole("alert")).textContent).toContain("could not be loaded");
});

test("the header shows the status in plain words and the clip count; Details has the format and lists a clip that is gone", async () => {
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
  expect(within(header).getByText("Editing…")).toBeTruthy();
  expect(within(header).getByText("1 clip")).toBeTruthy();
  expect(within(header).queryByText("9:16")).toBeNull();
  openDetails();
  const inside = within(details());
  expect(await inside.findByText("Open on the pose")).toBeTruthy();
  expect(inside.getByText("No longer available")).toBeTruthy();
  expect(inside.getByText("9:16")).toBeTruthy();
});

// ── Plain words, Details (the founder's "less dev" pass) ────────────────────

/** A project whose latest version was made by the v2 editor run: plan, two rounds of checks, the finish. */
function v2Api(extra: Record<string, object> = {}) {
  return api({
    ...noRenders,
    "GET /v1/projects/{id}/timelines": { items: [versionSummary(3, { summary: ["Sped c2 up to 2x"], author_id: runId(1) })], next_before: null },
    "GET /v1/runs/{id}": v2Run({ timeline_version: 3 }),
    "GET /v1/runs/{id}/artifacts": v2Artifacts(),
    ...extra,
  });
}

test("Details is collapsed by default: no steps, checks, costs, codes or ids until it is opened", async () => {
  v2Api();
  page();
  await reviewUp();
  await screen.findByTestId("what-i-made");
  expect(details().open).toBe(false);
  expect(within(details()).getByText("Details")).toBeTruthy();
  // None of the technical side is on the page.
  expect(screen.queryByTestId("run-cost")).toBeNull();
  expect(screen.queryByTestId("edit-checks")).toBeNull();
  expect(screen.queryByTestId("creative-plan")).toBeNull();
  expect(screen.queryByRole("list", { name: "Steps" })).toBeNull();
  expect(screen.queryByText(/\$\d/)).toBeNull();
  expect(screen.queryByText(/run_01|prj_01|toolu_|tool call|Opus/)).toBeNull();
  // The timeline is for commenting: no effect badges until Details is open.
  expect(document.querySelector('[data-slot="clip-badges"]')).toBeNull();

  openDetails();
  expect(details().open).toBe(true);
  expect(await screen.findByTestId("run-cost")).toBeTruthy();
  expect(screen.getByTestId("edit-checks")).toBeTruthy();
  expect(screen.getByTestId("creative-plan")).toBeTruthy();
  expect(within(details()).getByText(runId(1))).toBeTruthy();
  // Nothing was deleted, only moved: the closing note is not repeated in Details, and the run's own Stop or answer box is not there.
  expect(within(details()).queryByTestId("run-summary")).toBeNull();
});

test("a finished edit says what it made, the self-review in one line with the checks one click away, and the plan as a storyboard", async () => {
  v2Api();
  page();
  const made = await screen.findByTestId("what-i-made");
  expect(made.textContent).toContain("A 24 second vertical cut of Gino's back day");

  const review = screen.getByTestId("self-review");
  await waitFor(() => expect(review.textContent).toContain("I fixed 1 thing before showing you this"));
  const checked = review.querySelector("details") as HTMLDetailsElement;
  expect(checked.open).toBe(false);
  // The checks have plain names, never their ids.
  expect(within(review).getByText("Even brightness")).toBeTruthy();
  expect(review.textContent).not.toMatch(/brightness_jump|match_exposure/);

  const board = await screen.findByTestId("storyboard");
  expect(within(board).getByText("Opens on your heaviest lat pulldown lockout, with a quick zoom")).toBeTruthy();
  expect(within(board).getByText("Ends on your back pose, held, with a flash")).toBeTruthy();
  expect(board.textContent).not.toMatch(/BPM|beats|hook|punch_in/);
});

test("a failed edit says why in a sentence, that nothing was charged, and offers one thing to do; the code is only in Details", async () => {
  const fake = v2Api({
    "GET /v1/runs/{id}": run({ status: "failed", failure_reason: "budget", events: [event(1, "status", "failed reason=budget limit=dollars")], last_seq: 1 }),
    "POST /v1/projects/{id}/edit": json(202, run({ id: runId(6), status: "queued" })),
  });
  const onRunStarted = vi.fn();
  page({ onRunStarted });
  const card = await screen.findByTestId("edit-progress");
  expect(card.getAttribute("data-state")).toBe("failed");
  expect(within(card).getByRole("alert").textContent).toBe("This edit was bigger than I could finish in one go. Nothing was charged.");
  expect(card.textContent).not.toMatch(/budget|dollars|limit/);
  fireEvent.click(within(card).getByRole("button", { name: "Try again" }));
  await waitFor(() => expect(onRunStarted).toHaveBeenCalledWith(runId(6)));
  expect(fake.callsTo("POST /v1/projects/{id}/edit")[0]?.body).toEqual({});

  openDetails();
  expect(await within(details()).findByText("budget")).toBeTruthy();
});

test("Download makes the full video of the version on screen, follows it, and then links to the file", async () => {
  let made = false;
  const fake = api({
    ...noRenders,
    "GET /v1/projects/{id}/renders": (call: { query: URLSearchParams }) =>
      call.query.get("profile") === "final" ? { items: made ? [render({ id: renderId(9), timeline_version: 3, profile: "final", status: "rendering", progress: 30 })] : [], next_cursor: null } : apiError(404, "not_found"),
    "POST /v1/projects/{id}/renders": () => {
      made = true;
      return json(202, render({ id: renderId(9), timeline_version: 3, profile: "final", status: "queued" }));
    },
    "GET /v1/renders/{id}": readyRender({ id: renderId(9), timeline_version: 3, profile: "final" }),
  });
  page();
  fireEvent.click(await screen.findByRole("button", { name: "Download video" }));
  await waitFor(() => expect(fake.callsTo("POST /v1/projects/{id}/renders")[0]?.body).toEqual({ version: 3, profile: "final" }));
  const link = await screen.findByRole("link", { name: "Download video" });
  expect(link.getAttribute("href")).toBe("https://files.test/preview.mp4");
});

test("a download of work that was never paid for says what to do, without the code", async () => {
  api({
    ...noRenders,
    "GET /v1/projects/{id}/renders": (call: { query: URLSearchParams }) => (call.query.get("profile") === "final" ? { items: [], next_cursor: null } : apiError(404, "not_found")),
    "POST /v1/projects/{id}/renders": apiError(422, "version_not_charged"),
  });
  page();
  fireEvent.click(await screen.findByRole("button", { name: "Download video" }));
  const alert = await within(screen.getByTestId("download")).findByRole("alert");
  expect(alert.textContent).toMatch(/can't be downloaded yet.*Ask me for a change/);
  expect(alert.textContent).not.toContain("version_not_charged");
});
