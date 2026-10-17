import { act, screen, within } from "@testing-library/react";
import { createMemoryHistory, RouterProvider } from "@tanstack/react-router";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, expect, test } from "vitest";
import { getRouter } from "./router";

let root: Root | undefined;

afterEach(() => {
  act(() => root?.unmount());
  root = undefined;
});

function renderAt(path: string) {
  const router = getRouter(createMemoryHistory({ initialEntries: [path] }));
  root = createRoot(document);
  act(() => root?.render(<RouterProvider router={router} />));
  return router;
}

test("/library shows the sample library with a notice, and Library sits above Reports in the nav", async () => {
  renderAt("/library");
  expect(await screen.findByRole("heading", { name: "Library" })).toBeTruthy();
  expect(screen.getByRole("note").textContent).toBe("Sample clips until upload lands (P1-01/P1-02)");
  expect(await screen.findByText("Back double biceps pose")).toBeTruthy();
  const nav = within(document.querySelector('[data-slot="sidebar"]') as HTMLElement);
  const links = nav.getAllByRole("link").map((link) => link.textContent);
  expect(links.indexOf("Library")).toBeGreaterThan(-1);
  expect(links.indexOf("Library")).toBeLessThan(links.indexOf("Reports"));
  expect(nav.getByRole("link", { name: "Library" }).hasAttribute("data-active")).toBe(true);
});

test("?state= and ?samples=local keep working on the route", async () => {
  renderAt("/library?state=empty&samples=local");
  expect(await screen.findByText("Upload your first clips")).toBeTruthy();
  expect(screen.getByText(/Pick local videos/)).toBeTruthy();
});

test("?state=loading shows the skeletons, and an unknown state is ignored", async () => {
  renderAt("/library?state=loading");
  expect(await screen.findByText("Loading your clips")).toBeTruthy();
  act(() => root?.unmount());
  renderAt("/library?state=bogus");
  expect(await screen.findByText("Back double biceps pose")).toBeTruthy();
});
