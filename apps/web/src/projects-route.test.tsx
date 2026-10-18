import { act, cleanup, fireEvent, screen, waitFor, within } from "@testing-library/react";
import { createMemoryHistory, RouterProvider } from "@tanstack/react-router";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, expect, test, vi } from "vitest";
import { getRouter } from "./router";
import { apiError, installApi, json } from "./test-api";
import { ME } from "./test-clerk";
import { apiAsset, clip, project, projectId, projectSummary, run, runId, timelineVersion, versionSummary } from "./test-fixtures";

let root: Root | undefined;

afterEach(() => {
  act(() => root?.unmount());
  root = undefined;
  cleanup();
});

function renderAt(path: string) {
  const router = getRouter(createMemoryHistory({ initialEntries: [path] }));
  root = createRoot(document);
  act(() => root?.render(<RouterProvider router={router} />));
  return router;
}

const sidebarLink = (name: string) => within(document.querySelector('[data-slot="sidebar"]') as HTMLElement).getByRole("link", { name });

test("/projects lists the projects as cards that open the project, and the nav has Projects above New video", async () => {
  installApi({
    "GET /v1/me": ME,
    "GET /v1/projects": {
      items: [
        projectSummary(1, { title: "Back day", status: "review", current_timeline_version: 3, asset_count: 4 }),
        projectSummary(2, { title: "Leg day", format: "16:9", status: "editing", asset_count: 1 }),
      ],
      next_cursor: null,
    },
  });
  renderAt("/projects");
  expect(await screen.findByRole("heading", { name: "Projects", level: 1 })).toBeTruthy();
  const card = await screen.findByRole("link", { name: /Back day/ });
  expect(card.getAttribute("href")).toBe(`/projects/${projectId(1)}`);
  expect(card.textContent).toContain("4 clips");
  // Where it is, in plain words: no "v3", no format codes.
  expect(card.textContent).toContain("Ready to watch");
  expect(card.textContent).not.toContain("v3");
  const second = screen.getByRole("link", { name: /Leg day/ });
  expect(second.textContent).toContain("1 clip");
  expect(second.textContent).toContain("Editing…");

  const links = within(document.querySelector('[data-slot="sidebar"]') as HTMLElement).getAllByRole("link").map((link) => link.textContent);
  expect(links.indexOf("Projects")).toBeGreaterThan(-1);
  expect(links.indexOf("Projects")).toBeLessThan(links.indexOf("New video"));
  expect(sidebarLink("Projects").hasAttribute("data-active")).toBe(true);
  expect(sidebarLink("New video").hasAttribute("data-active")).toBe(false);
});

test("a project card shows a frame of its first clip that is still there", async () => {
  installApi({
    "GET /v1/me": ME,
    "GET /v1/projects": { items: [projectSummary(1, { title: "Back day", status: "review", current_timeline_version: 1 })], next_cursor: null },
    "GET /v1/projects/{id}": project(1, { title: "Back day" }),
    "GET /v1/assets": { items: [apiAsset(1), apiAsset(2)], next_cursor: null },
  });
  renderAt("/projects");
  const card = await screen.findByRole("link", { name: /Back day/ });
  await waitFor(() => expect(card.querySelector("img")?.getAttribute("src")).toBe("https://cdn.test/poster-1.jpg"));
});

test("/projects with none offers to start the first one", async () => {
  installApi({ "GET /v1/me": ME, "GET /v1/projects": { items: [], next_cursor: null } });
  renderAt("/projects");
  expect(await screen.findByText("No projects yet")).toBeTruthy();
  expect(screen.getByRole("link", { name: "Make your first video" }).getAttribute("href")).toBe("/projects/new");
});

test("/projects pages through the list, and says when it cannot be loaded", async () => {
  const api = installApi({
    "GET /v1/me": ME,
    "GET /v1/projects": (call) =>
      call.query.get("cursor") ? { items: [projectSummary(2, { title: "Second page" })], next_cursor: null } : { items: [projectSummary(1, { title: "First page" })], next_cursor: "c2" },
  });
  renderAt("/projects");
  fireEvent.click(await screen.findByRole("button", { name: "Show more projects" }));
  expect(await screen.findByText("Second page")).toBeTruthy();
  expect(screen.getByText("First page")).toBeTruthy();
  expect(screen.queryByRole("button", { name: "Show more projects" })).toBeNull();
  expect(api.callsTo("GET /v1/projects")).toHaveLength(2);
});

test("/projects shows the error and tries again", async () => {
  const api = installApi({ "GET /v1/me": ME, "GET /v1/projects": apiError(403, "no_active_workspace") });
  renderAt("/projects");
  expect((await screen.findByRole("alert")).textContent).toContain("could not be loaded");
  api.on("GET /v1/projects", { items: [projectSummary(1, { title: "Back day" })], next_cursor: null });
  fireEvent.click(screen.getByRole("button", { name: "Try again" }));
  expect(await screen.findByText("Back day")).toBeTruthy();
});

test("/projects/new keeps its own nav item and heading, and /projects/<id> belongs to Projects", async () => {
  installApi({ "GET /v1/me": ME, "GET /v1/assets": { items: [apiAsset(1)], next_cursor: null } });
  renderAt("/projects/new");
  expect(await screen.findByRole("heading", { name: "New video" })).toBeTruthy();
  expect(sidebarLink("New video").hasAttribute("data-active")).toBe(true);
  expect(sidebarLink("Projects").hasAttribute("data-active")).toBe(false);
  act(() => root?.unmount());

  const id = projectId(1);
  installApi({
    "GET /v1/me": ME,
    "GET /v1/projects/{id}": project(1, { title: "Back day" }),
    "GET /v1/projects/{id}/timelines": { items: [], next_before: null },
  });
  renderAt(`/projects/${id}`);
  expect(await screen.findByRole("heading", { name: "Back day" })).toBeTruthy();
  expect(sidebarLink("Projects").hasAttribute("data-active")).toBe(true);
  expect(sidebarLink("New video").hasAttribute("data-active")).toBe(false);
});

test("?run= on a project page follows that run; a value that is not a run id is ignored", async () => {
  const api = installApi({
    "GET /v1/me": ME,
    "GET /v1/projects/{id}": project(1),
    "GET /v1/projects/{id}/timelines": { items: [], next_before: null },
    "GET /v1/runs/{id}": run({ id: runId(3), status: "succeeded", summary: "All done." }),
  });
  renderAt(`/projects/${projectId(1)}?run=${runId(3)}`);
  await waitFor(() => expect(api.callsTo("GET /v1/runs/{id}")[0]?.path).toBe(`/v1/runs/${runId(3)}`));
  expect(await screen.findByRole("heading", { name: "No video yet" })).toBeTruthy();
  act(() => root?.unmount());

  const before = api.callsTo("GET /v1/runs/{id}").length;
  renderAt(`/projects/${projectId(1)}?run=not-a-run`);
  expect(await screen.findByRole("heading", { name: "No video yet" })).toBeTruthy();
  expect(api.callsTo("GET /v1/runs/{id}")).toHaveLength(before);
});

test("creating a project opens its page on the run that was started", async () => {
  const api = installApi({
    "GET /v1/me": ME,
    "GET /v1/assets": { items: [apiAsset(1, { title: "Back pose" })], next_cursor: null },
    "POST /v1/projects": json(201, project(1)),
    "POST /v1/projects/{id}/edit": json(202, run({ id: runId(8), status: "queued" })),
    "GET /v1/projects/{id}": project(1, { title: "Back pose project" }),
    "GET /v1/projects/{id}/timelines": { items: [], next_before: null },
    "GET /v1/runs/{id}": run({ id: runId(8), status: "running" }),
  });
  const router = renderAt("/projects/new");
  fireEvent.click(await screen.findByRole("button", { name: /^Next/ }));
  fireEvent.click(await screen.findByRole("checkbox", { name: /Back pose/ }));
  fireEvent.click(screen.getByRole("button", { name: /^Next/ }));
  fireEvent.click(screen.getAllByRole("button", { name: /^Review/ }).at(-1) as HTMLElement);
  fireEvent.click(screen.getByRole("button", { name: "Make my video" }));

  await waitFor(() => expect(router.state.location.pathname).toBe(`/projects/${projectId(1)}`));
  expect(router.state.location.search).toEqual({ run: runId(8) });
  expect(await screen.findByRole("heading", { name: "I'm making your video" })).toBeTruthy();
  expect(api.callsTo("POST /v1/projects/{id}/edit")).toHaveLength(1);
});

test("a project page whose versions exist shows the review for the route's project", async () => {
  installApi({
    "GET /v1/me": ME,
    "GET /v1/projects/{id}": project(1, { title: "Back day", current_timeline_version: 1 }),
    "GET /v1/projects/{id}/timelines": { items: [versionSummary(1)], next_before: null },
    "GET /v1/projects/{id}/timelines/{version}": timelineVersion(1, [clip("c1", 1, 0, 0, 4000)]),
    "GET /v1/runs/{id}": run({ status: "succeeded", summary: "Done." }),
    "GET /v1/projects/{id}/renders": apiError(404, "not_found"),
  });
  vi.spyOn(HTMLMediaElement.prototype, "play").mockResolvedValue(undefined);
  renderAt(`/projects/${projectId(1)}`);
  expect(await screen.findByRole("group", { name: "Version" })).toBeTruthy();
  expect(screen.getByRole("button", { name: "Make the changes" })).toBeTruthy();
});
