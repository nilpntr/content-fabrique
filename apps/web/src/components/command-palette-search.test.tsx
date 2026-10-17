import { act, cleanup, fireEvent, screen, waitFor } from "@testing-library/react";
import { createMemoryHistory, RouterProvider } from "@tanstack/react-router";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, test } from "vitest";
import { getRouter } from "@/router";
import { installApi } from "@/test-api";
import { ME } from "@/test-clerk";
import { apiAsset, projectSummary } from "@/test-fixtures";

let root: Root | undefined;

beforeEach(() => {
  Object.defineProperty(Element.prototype, "getAnimations", { configurable: true, value: () => [] });
});
afterEach(() => {
  act(() => root?.unmount());
  root = undefined;
  Reflect.deleteProperty(Element.prototype, "getAnimations");
  cleanup();
});

async function openPalette() {
  const router = getRouter(createMemoryHistory({ initialEntries: ["/reports"] }));
  root = createRoot(document);
  act(() => root?.render(<RouterProvider router={router} />));
  await screen.findByRole("heading", { name: "Reports" });
  fireEvent.keyDown(window, { key: "k", ctrlKey: true });
  return { router, input: (await screen.findByPlaceholderText("Search projects, clips and pages…")) as HTMLInputElement };
}

const options = () => screen.queryAllByRole("option").map((option) => option.textContent);

test("⌘K finds projects and clips by what the library knows of them, next to the pages", async () => {
  const api = installApi({
    "GET /v1/me": ME,
    "GET /v1/projects": { items: [projectSummary(1, { title: "Gino's back day", status: "review" }), projectSummary(2, { title: "Legs" })], next_cursor: null },
    "GET /v1/assets": {
      items: [apiAsset(1, { title: "Seated cable row", tags: ["back day"] }), apiAsset(2, { title: "Squat", description: "A heavy squat, deep", tags: [] })],
      next_cursor: null,
    },
  });
  const { input } = await openPalette();
  // The lists are asked for once the palette is open, not before.
  await waitFor(() => expect(api.callsTo("GET /v1/projects")).toHaveLength(1));
  expect(api.callsTo("GET /v1/assets")).toHaveLength(1);

  fireEvent.change(input, { target: { value: "back" } });
  await waitFor(() => expect(options()).toHaveLength(2));
  expect(options()[0]).toContain("Gino's back day");
  expect(options()[0]).toContain("9:16 · In review");
  expect(options()[1]).toContain("Seated cable row");
  expect(screen.getByText("Projects", { selector: "div[aria-hidden]" })).toBeTruthy();
  expect(screen.getByText("Clips", { selector: "div[aria-hidden]" })).toBeTruthy();

  // What the quick look wrote is searched too.
  fireEvent.change(input, { target: { value: "heavy squat" } });
  await waitFor(() => expect(options()).toHaveLength(1));
  expect(options()[0]).toContain("Squat");
});

test("choosing a project opens it, and a query that matches nothing says so", async () => {
  installApi({ "GET /v1/me": ME, "GET /v1/projects": { items: [projectSummary(1, { title: "Gino's back day" })], next_cursor: null }, "GET /v1/assets": { items: [], next_cursor: null } });
  const { router, input } = await openPalette();
  fireEvent.change(input, { target: { value: "gino" } });
  await waitFor(() => expect(options()).toHaveLength(1));
  await act(async () => {
    fireEvent.click(screen.getAllByRole("option")[0] as HTMLElement);
  });
  await waitFor(() => expect(router.state.location.pathname).toMatch(/^\/projects\/prj_/));

  fireEvent.keyDown(window, { key: "k", ctrlKey: true });
  const again = (await screen.findByPlaceholderText("Search projects, clips and pages…")) as HTMLInputElement;
  fireEvent.change(again, { target: { value: "zzzz" } });
  await screen.findByText("No results for “zzzz”.");
});
