import { act, fireEvent, screen, waitFor, within } from "@testing-library/react";
import { createMemoryHistory, RouterProvider } from "@tanstack/react-router";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, expect, test } from "vitest";
import { getRouter } from "./router";
import { installApi } from "./test-api";
import { ME } from "./test-clerk";
import { apiAsset } from "./test-fixtures";

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

test("/library with no clips says how to add the first ones, never shows made-up clips, and Library sits above Settings in the nav", async () => {
  installApi({ "GET /v1/me": ME, "GET /v1/assets": { items: [], next_cursor: null } });
  renderAt("/library");
  expect(await screen.findByRole("heading", { name: "Library" })).toBeTruthy();
  expect(await screen.findByText("Upload your first clips")).toBeTruthy();
  expect(screen.queryByText("Back double biceps pose")).toBeNull();
  expect(screen.queryByText(/Sample clips/)).toBeNull();
  const nav = within(document.querySelector('[data-slot="sidebar"]') as HTMLElement);
  const links = nav.getAllByRole("link").map((link) => link.textContent);
  expect(links.indexOf("Library")).toBeGreaterThan(-1);
  expect(links.indexOf("Library")).toBeLessThan(links.indexOf("Settings"));
  expect(nav.getByRole("link", { name: "Library" }).hasAttribute("data-active")).toBe(true);
});

test("?state= and ?samples=local keep working on the route", async () => {
  renderAt("/library?state=empty&samples=local");
  expect(await screen.findByText("Upload your first clips")).toBeTruthy();
  expect(screen.getByText(/Pick local videos/)).toBeTruthy();
});

test("?open=<asset id> opens that clip's sheet, closing it clears the address, and anything else is ignored", async () => {
  const clips = [apiAsset(1, { title: "Seated cable row", description: "Controlled rows.", quick_look_status: "succeeded" }), apiAsset(2, { title: "Back pose" })];
  installApi({ "GET /v1/me": ME, "GET /v1/assets": { items: clips, next_cursor: null } });
  const router = renderAt(`/library?open=${clips[1]?.id}`);
  expect(await screen.findByRole("heading", { name: "Back pose" })).toBeTruthy();
  expect(screen.getByRole("dialog")).toBeTruthy();

  await act(async () => {
    fireEvent.click(screen.getByRole("button", { name: "Close" }));
  });
  await waitFor(() => expect((router.state.location.search as { open?: string }).open).toBeUndefined());
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());

  // Opening another from the grid puts it in the address.
  await act(async () => {
    fireEvent.click(screen.getByRole("button", { name: "Open Seated cable row" }));
  });
  await waitFor(() => expect((router.state.location.search as { open?: string }).open).toBe(clips[0]?.id));

  act(() => root?.unmount());
  const other = renderAt("/library?open=not-an-asset");
  await screen.findByText("Back pose");
  expect(screen.queryByRole("dialog")).toBeNull();
  expect((other.state.location.search as { open?: string }).open).toBeUndefined();
});

test("?state=loading shows the skeletons, and an unknown state is ignored", async () => {
  renderAt("/library?state=loading");
  expect(await screen.findByText("Loading your clips")).toBeTruthy();
  act(() => root?.unmount());
  installApi({ "GET /v1/me": ME, "GET /v1/assets": { items: [], next_cursor: null } });
  renderAt("/library?state=bogus");
  // An unknown state is the real library (empty here), not the samples.
  expect(await screen.findByText("Upload your first clips")).toBeTruthy();
  expect(screen.queryByText("Back double biceps pose")).toBeNull();
});
