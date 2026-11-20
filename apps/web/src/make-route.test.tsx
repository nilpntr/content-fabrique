import { act, cleanup, fireEvent, screen, waitFor, within } from "@testing-library/react";
import { createMemoryHistory, RouterProvider } from "@tanstack/react-router";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, expect, test } from "vitest";
import { getRouter } from "./router";
import { installApi } from "./test-api";
import { ME } from "./test-clerk";
import { event, project, projectId, run, runId } from "./test-fixtures";

let root: Root | undefined;

afterEach(() => {
  act(() => root?.unmount());
  root = undefined;
  cleanup();
  window.localStorage.clear();
});

function renderAt(path: string) {
  const router = getRouter(createMemoryHistory({ initialEntries: [path] }));
  root = createRoot(document);
  act(() => root?.render(<RouterProvider router={router} />));
  return router;
}

// The first render of the whole app takes a moment on a busy machine.
const SLOW = { timeout: 5000 };
const sidebarLink = (name: string) => within(document.querySelector('[data-slot="sidebar"]') as HTMLElement).getByRole("link", { name });

test("/make is 'Make a video' in the sidebar and opens on the clips", async () => {
  installApi({ "GET /v1/me": ME, "GET /v1/assets": { items: [], next_cursor: null } });
  renderAt("/make");
  expect(await screen.findByRole("heading", { name: "Add your clips" }, SLOW)).toBeTruthy();
  expect(sidebarLink("Make a video").getAttribute("href")).toBe("/make");
  expect(sidebarLink("Make a video").hasAttribute("data-active")).toBe(true);
  expect(sidebarLink("Advanced").hasAttribute("data-active")).toBe(false);
  // The old new-project page stays reachable: a nav item and a quiet link at the bottom of the clips screen.
  expect(sidebarLink("Advanced").getAttribute("href")).toBe("/projects/new");
  expect(screen.getAllByRole("link", { name: "Advanced" }).map((a) => a.getAttribute("href"))).toEqual(["/projects/new", "/projects/new"]);
});

test("/make/<id>?run= follows that edit, stays under 'Make a video', and 'Start over' goes back to /make", async () => {
  const api = installApi({
    "GET /v1/me": ME,
    "GET /v1/assets": { items: [], next_cursor: null },
    "GET /v1/projects/{id}": project(1),
    "GET /v1/projects/{id}/timelines": { items: [], next_before: null },
    "GET /v1/runs/{id}": run({ id: runId(3), status: "running", events: [event(1, "tool_call", "find_moments", { tool: "find_moments" })] }),
  });
  renderAt(`/make/${projectId(1)}?run=${runId(3)}`);
  expect(await screen.findByRole("heading", { name: "I'm making your video" }, SLOW)).toBeTruthy();
  expect(screen.getByText("Finding your best moments…")).toBeTruthy();
  expect(api.callsTo("GET /v1/runs/{id}")[0]?.path).toBe(`/v1/runs/${runId(3)}`);
  expect(sidebarLink("Make a video").hasAttribute("data-active")).toBe(true);
  act(() => root?.unmount());

  // A value that is not an edit's id is ignored: with no edit and no video, the screen offers to make it, or to start over.
  renderAt(`/make/${projectId(1)}?run=nope`);
  fireEvent.click(await screen.findByRole("button", { name: "Start over" }, SLOW));
  await waitFor(() => expect(screen.getByRole("heading", { name: "Add your clips" })).toBeTruthy());
});
