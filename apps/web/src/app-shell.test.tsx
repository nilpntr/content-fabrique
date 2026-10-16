import { act, screen, within } from "@testing-library/react";
import { createMemoryHistory, createRouter, RouterProvider } from "@tanstack/react-router";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, expect, test } from "vitest";
import { routeTree } from "./routeTree.gen";

let root: Root | undefined;

afterEach(() => {
  act(() => root?.unmount());
  root = undefined;
});

// The root route is the whole document (<html> and up) and supplies the providers, so render it into
// `document` the way Start hydrates it.
function renderAt(path: string) {
  const router = createRouter({ routeTree, history: createMemoryHistory({ initialEntries: [path] }) });
  root = createRoot(document);
  act(() => root?.render(<RouterProvider router={router} />));
  return router;
}

test("renders the shell with the sidebar nav and the current page", async () => {
  renderAt("/reports");
  expect(await screen.findByRole("heading", { name: "Reports" })).toBeTruthy();
  expect(screen.getByText("Dashboard")).toBeTruthy();
  expect(screen.getByText("jane@example.com")).toBeTruthy();
});

test("the lens marks the current page and moves with navigation", async () => {
  const router = renderAt("/reports");
  const nav = within(await screen.findByRole("navigation", { name: "Main" }));
  const reports = nav.getByRole("link", { name: "Reports" });
  expect(reports.hasAttribute("data-active")).toBe(true);
  expect(reports.querySelector('[data-slot="sidebar-menu-lens"]')).toBeTruthy();

  await act(() => router.navigate({ to: "/settings" }));
  expect(await screen.findByRole("heading", { name: "Settings" })).toBeTruthy();
  expect(nav.getByRole("link", { name: "Settings" }).hasAttribute("data-active")).toBe(true);
  expect(nav.getByRole("link", { name: "Reports" }).hasAttribute("data-active")).toBe(false);
});

test("keyboard focus moves through the nav", async () => {
  renderAt("/reports");
  const nav = within(await screen.findByRole("navigation", { name: "Main" }));
  const overview = nav.getByRole("link", { name: "Overview" });
  const settings = nav.getByRole("link", { name: "Settings" });
  act(() => overview.focus());
  expect(document.activeElement).toBe(overview);
  act(() => settings.focus());
  expect(document.activeElement).toBe(settings);
});
