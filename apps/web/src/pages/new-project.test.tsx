import { cleanup, fireEvent, screen, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, expect, test, vi } from "vitest";
import { clerkMock } from "@/test-clerk";
import { apiError, installApi, json, renderApp } from "@/test-api";
import { apiAsset, assetId, learntStyle, project, projectId, run, runId } from "@/test-fixtures";
import { NewProjectPage, toPickerAsset } from "./new-project";

vi.mock("@tanstack/react-router", async (original) => ({
  ...(await original<typeof import("@tanstack/react-router")>()),
  Link: ({ to, children, className }: { to: string; children: ReactNode; className?: string }) => (
    <a href={to} className={className}>
      {children}
    </a>
  ),
}));
const notify = vi.fn();
vi.mock("@/components/ui/toast", () => ({ notify: (...args: unknown[]) => notify(...args) }));

afterEach(() => {
  cleanup();
  notify.mockReset();
});

// Library: two ready videos, one still processing, one ready image, one failed video.
const LIBRARY = {
  items: [
    apiAsset(1, { title: "Back pose" }),
    apiAsset(2, { title: "Barbell row", tags: ["exercise"] }),
    apiAsset(3, { title: "Still processing", status: "processing", derivatives: undefined, media: undefined }),
    apiAsset(4, { title: "A photo", kind: "image" }),
    apiAsset(5, { title: "Broken", status: "failed", failure_reason: "corrupt_media" }),
  ],
  next_cursor: null,
};

async function pickTwoAndReview(brief = "Open on the pose", name = "") {
  fireEvent.click(await screen.findByRole("button", { name: /^Next/ }));
  fireEvent.click(await screen.findByRole("checkbox", { name: /Back pose/ }));
  fireEvent.click(screen.getByRole("checkbox", { name: /Barbell row/ }));
  fireEvent.click(screen.getByRole("button", { name: /^Next/ }));
  if (name) fireEvent.change(screen.getByRole("textbox", { name: /^Name/ }), { target: { value: name } });
  if (brief) fireEvent.change(screen.getByRole("textbox", { name: /^What you want/ }), { target: { value: brief } });
  fireEvent.click(screen.getAllByRole("button", { name: /^Review/ }).at(-1) as HTMLElement);
}

test("only ready videos of the library are offered, with their posters", async () => {
  installApi({ "GET /v1/assets": LIBRARY });
  renderApp(<NewProjectPage onOpenProject={vi.fn()} />);
  fireEvent.click(await screen.findByRole("button", { name: /^Next/ }));
  const boxes = (await screen.findAllByRole("checkbox")).map((box) => box.getAttribute("aria-labelledby"));
  expect(boxes).toHaveLength(2);
  expect(screen.getByRole("checkbox", { name: /Back pose/ })).toBeTruthy();
  expect(screen.getByRole("checkbox", { name: /Barbell row/ })).toBeTruthy();
  expect(screen.queryByRole("checkbox", { name: /Still processing|A photo|Broken/ })).toBeNull();
  expect(document.querySelector('img[src="https://cdn.test/poster-1.jpg"]')).toBeTruthy();
});

test("a hover over a card plays its preview, and the loop is not fetched before", async () => {
  installApi({ "GET /v1/assets": LIBRARY });
  renderApp(<NewProjectPage onOpenProject={vi.fn()} />);
  fireEvent.click(await screen.findByRole("button", { name: /^Next/ }));
  const card = (await screen.findByRole("checkbox", { name: /Back pose/ })).closest("li") as HTMLElement;
  expect(document.querySelector("video")).toBeNull();
  fireEvent.pointerEnter(card, { pointerType: "mouse" });
  expect(document.querySelector("video")?.getAttribute("src")).toBe("https://cdn.test/hover-1.mp4");
  fireEvent.pointerLeave(card);
  expect(document.querySelector("video")).toBeNull();
});

test("creating makes the project, starts the editor with the instructions and opens the project on that run", async () => {
  const api = installApi({
    "GET /v1/assets": LIBRARY,
    "POST /v1/projects": json(201, project(1)),
    "POST /v1/projects/{id}/edit": json(202, run({ id: runId(7), status: "queued" })),
  });
  const onOpen = vi.fn();
  renderApp(<NewProjectPage onOpenProject={onOpen} />);
  await pickTwoAndReview("Open on the pose", "Gym day");

  // What it uses is said in videos, never in dollars.
  expect(screen.getByText("Uses")).toBeTruthy();
  expect(screen.getByText(/About one video from your plan/)).toBeTruthy();
  expect(screen.queryByText(/\$\d/)).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: "Make my video" }));

  await waitFor(() => expect(onOpen).toHaveBeenCalledWith(projectId(1), runId(7)));
  expect(api.callsTo("POST /v1/projects")[0]?.body).toEqual({ format: "9:16", asset_ids: [assetId(1), assetId(2)], title: "Gym day", brief: "Open on the pose" });
  const edit = api.callsTo("POST /v1/projects/{id}/edit");
  expect(edit).toHaveLength(1);
  expect(edit[0]?.path).toBe(`/v1/projects/${projectId(1)}/edit`);
  expect(edit[0]?.body).toEqual({ instructions: "Open on the pose" });
});

test("without instructions the editor starts with an empty request and no brief is sent", async () => {
  const api = installApi({
    "GET /v1/assets": LIBRARY,
    "POST /v1/projects": json(201, project(1)),
    "POST /v1/projects/{id}/edit": json(202, run({ id: runId(2) })),
  });
  const onOpen = vi.fn();
  renderApp(<NewProjectPage onOpenProject={onOpen} />);
  await pickTwoAndReview("");
  fireEvent.click(screen.getByRole("button", { name: "Make my video" }));
  await waitFor(() => expect(onOpen).toHaveBeenCalled());
  expect(api.callsTo("POST /v1/projects")[0]?.body).toEqual({ format: "9:16", asset_ids: [assetId(1), assetId(2)] });
  expect(api.callsTo("POST /v1/projects/{id}/edit")[0]?.body).toEqual({});
});

test.each([
  ["422 assets_not_usable", apiError(422, "assets_not_usable", "Clip 3 is still processing."), /aren't ready yet.*Clip 3 is still processing/],
  ["404 not_found", apiError(404, "not_found"), /has been deleted/],
  ["402 plan_required", apiError(402, "plan_required"), /need an active plan/],
  ["403 insufficient_role", apiError(403, "insufficient_role"), /can't start new videos/],
  ["500", apiError(500, "internal_error"), /could not be started/],
])("a refused project (%s) says why, stays on the flow and starts no editor", async (_name, reply, message) => {
  const api = installApi({ "GET /v1/assets": LIBRARY, "POST /v1/projects": reply });
  const onOpen = vi.fn();
  renderApp(<NewProjectPage onOpenProject={onOpen} />);
  await pickTwoAndReview();
  fireEvent.click(screen.getByRole("button", { name: "Make my video" }));
  expect((await screen.findByRole("alert")).textContent).toMatch(message);
  expect(onOpen).not.toHaveBeenCalled();
  expect(api.callsTo("POST /v1/projects/{id}/edit")).toHaveLength(0);
  // The button is back for another try.
  await waitFor(() => expect(screen.getByRole("button", { name: "Make my video" }).hasAttribute("disabled")).toBe(false));
});

test("a project that was made but whose editor did not start opens without a run, and a toast says why", async () => {
  installApi({
    "GET /v1/assets": LIBRARY,
    "POST /v1/projects": json(201, project(1)),
    "POST /v1/projects/{id}/edit": apiError(422, "assets_not_usable"),
  });
  const onOpen = vi.fn();
  renderApp(<NewProjectPage onOpenProject={onOpen} />);
  await pickTwoAndReview();
  fireEvent.click(screen.getByRole("button", { name: "Make my video" }));
  await waitFor(() => expect(onOpen).toHaveBeenCalledWith(projectId(1)));
  expect(notify).toHaveBeenCalledWith("Your project is saved, but I couldn't start editing", expect.objectContaining({ type: "error", description: expect.stringMatching(/clips can be used/) }));
});

test("a run already in progress is explained", async () => {
  installApi({
    "GET /v1/assets": LIBRARY,
    "POST /v1/projects": json(201, project(1)),
    "POST /v1/projects/{id}/edit": apiError(409, "run_in_progress"),
  });
  renderApp(<NewProjectPage onOpenProject={vi.fn()} />);
  await pickTwoAndReview();
  fireEvent.click(screen.getByRole("button", { name: "Make my video" }));
  await waitFor(() => expect(notify).toHaveBeenCalled());
  expect(notify).toHaveBeenCalledWith(expect.any(String), expect.objectContaining({ description: expect.stringMatching(/still working/) }));
});

test("a library with no ready video points to the library, and says when clips are still processing", async () => {
  installApi({ "GET /v1/assets": { items: [apiAsset(4, { kind: "image" })], next_cursor: null } });
  renderApp(<NewProjectPage onOpenProject={vi.fn()} />);
  expect(await screen.findByRole("heading", { name: "Upload some clips first" })).toBeTruthy();
  expect(screen.getByRole("link", { name: "Upload clips" }).getAttribute("href")).toBe("/library");
  cleanup();

  installApi({ "GET /v1/assets": { items: [apiAsset(3, { status: "processing" })], next_cursor: null } });
  renderApp(<NewProjectPage onOpenProject={vi.fn()} />);
  expect(await screen.findByRole("heading", { name: "Your clips are almost ready" })).toBeTruthy();
});

test("a library that cannot be loaded offers another try", async () => {
  const api = installApi({ "GET /v1/assets": apiError(403, "no_active_workspace") });
  renderApp(<NewProjectPage onOpenProject={vi.fn()} />);
  expect((await screen.findByRole("alert")).textContent).toMatch(/could not be loaded/);
  api.on("GET /v1/assets", LIBRARY);
  fireEvent.click(screen.getByRole("button", { name: "Try again" }));
  expect(await screen.findByRole("button", { name: /^Next/ })).toBeTruthy();
});

test("every page of the library is fetched, so the picker can search all of it", async () => {
  const api = installApi({
    "GET /v1/assets": (call) =>
      call.query.get("cursor") ? { items: [apiAsset(2, { title: "On page two" })], next_cursor: null } : { items: [apiAsset(1, { title: "On page one" })], next_cursor: "page2" },
  });
  renderApp(<NewProjectPage onOpenProject={vi.fn()} />);
  fireEvent.click(await screen.findByRole("button", { name: /^Next/ }));
  expect(await screen.findByRole("checkbox", { name: /On page two/ })).toBeTruthy();
  expect(screen.getByRole("checkbox", { name: /On page one/ })).toBeTruthy();
  expect(api.callsTo("GET /v1/assets")).toHaveLength(2);
});

test("a page of the library that fails stops the paging and offers to load the rest", async () => {
  const api = installApi({
    "GET /v1/assets": (call) => (call.query.get("cursor") ? apiError(403, "no_active_workspace") : { items: [apiAsset(1, { title: "On page one" })], next_cursor: "page2" }),
  });
  renderApp(<NewProjectPage onOpenProject={vi.fn()} />);
  expect((await screen.findByRole("alert")).textContent).toContain("Some of your clips could not be loaded");
  fireEvent.click(await screen.findByRole("button", { name: /^Next/ }));
  expect(screen.getByRole("checkbox", { name: /On page one/ })).toBeTruthy();
  const calls = api.callsTo("GET /v1/assets").length;
  await new Promise((resolve) => setTimeout(resolve, 150));
  expect(api.callsTo("GET /v1/assets")).toHaveLength(calls);

  api.on("GET /v1/assets", (call) => (call.query.get("cursor") ? { items: [apiAsset(2, { title: "On page two" })], next_cursor: null } : { items: [apiAsset(1, { title: "On page one" })], next_cursor: "page2" }));
  fireEvent.click(screen.getByRole("button", { name: "Load the rest" }));
  expect(await screen.findByRole("checkbox", { name: /On page two/ })).toBeTruthy();
});

test("while the session is not ready the page is loading, not an empty library", async () => {
  clerkMock.session = { ...clerkMock.session, orgId: null };
  installApi({ "GET /v1/assets": { items: [], next_cursor: null } });
  renderApp(<NewProjectPage onOpenProject={vi.fn()} />);
  expect(await screen.findByText("Loading your clips")).toBeTruthy();
  expect(screen.queryByRole("heading", { name: "Upload some clips first" })).toBeNull();
});

test("toPickerAsset names a clip by its title, or its file when it has none, and carries the previews", () => {
  expect(toPickerAsset(apiAsset(1, { title: "" })).title).toBe("clip-1.mp4");
  expect(toPickerAsset(apiAsset(1))).toMatchObject({ id: assetId(1), posterUrl: "https://cdn.test/poster-1.jpg", hoverPreviewUrl: "https://cdn.test/hover-1.mp4", durationMs: 11_000 });
});

test("the vibe starts on the creator's style, and only a vibe picked for this video is sent", async () => {
  const style = learntStyle();
  const api = installApi({
    "GET /v1/assets": LIBRARY,
    "GET /v1/style": { ...style, effective: { ...style.effective, vibe: "calm", sources: { ...style.effective.sources, vibe: "you" } } },
    "POST /v1/projects": json(201, project(1)),
    "POST /v1/projects/{id}/edit": json(202, run({ id: runId(3) })),
  });
  const onOpen = vi.fn();
  renderApp(<NewProjectPage onOpenProject={onOpen} />);
  fireEvent.click(await screen.findByRole("button", { name: /^Next/ }));
  fireEvent.click(await screen.findByRole("checkbox", { name: /Back pose/ }));
  fireEvent.click(screen.getByRole("button", { name: /^Next/ }));
  // The picker shows the style's vibe and says where it came from.
  await waitFor(() => expect((screen.getByRole("radio", { name: /Calm/ }) as HTMLInputElement).checked).toBe(true));
  expect(screen.getByText("From your style.")).toBeTruthy();
  fireEvent.click(screen.getByRole("radio", { name: /Hype/ }));
  expect(screen.getByText("Just for this video.")).toBeTruthy();
  fireEvent.click(screen.getAllByRole("button", { name: /^Review/ }).at(-1) as HTMLElement);
  expect(screen.getByText("Hype")).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "Make my video" }));
  await waitFor(() => expect(onOpen).toHaveBeenCalled());
  expect(api.callsTo("POST /v1/projects/{id}/edit")[0]?.body).toEqual({ vibe: "hype" });
});
