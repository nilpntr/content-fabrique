import { sampleFacets } from "@/lib/sample-facets";
import { act, cleanup, fireEvent, screen, waitFor } from "@testing-library/react";
import { createMemoryHistory, RouterProvider } from "@tanstack/react-router";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, test } from "vitest";
import { getRouter } from "@/router";
import { apiError, installApi } from "@/test-api";
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
  const router = getRouter(createMemoryHistory({ initialEntries: ["/settings"] }));
  root = createRoot(document);
  act(() => root?.render(<RouterProvider router={router} />));
  await screen.findByRole("heading", { name: "Settings" });
  fireEvent.keyDown(window, { key: "k", ctrlKey: true });
  return { router, input: (await screen.findByPlaceholderText("Search your videos, clips and pages…")) as HTMLInputElement };
}

const options = () => screen.queryAllByRole("option").map((option) => option.textContent);

test("⌘K finds projects and clips by what the library knows of them, next to the pages", async () => {
  const api = installApi({
    "GET /v1/me": ME,
    "GET /v1/projects": { items: [projectSummary(1, { title: "Gino's back day", status: "review" }), projectSummary(2, { title: "Legs" })], next_cursor: null },
    "GET /v1/assets": {
      items: [apiAsset(1, { title: "Seated cable row", facets: sampleFacets("type:lift", "body:back") }), apiAsset(2, { title: "Squat", description: "A heavy squat, deep" })],
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
  expect(options()[0]).toContain("Ready to watch");
  expect(options()[1]).toContain("Seated cable row");
  expect(screen.getByText("Projects", { selector: "div[aria-hidden]" })).toBeTruthy();
  expect(screen.getByText("Clips", { selector: "div[aria-hidden]" })).toBeTruthy();

  // What the quick look wrote is searched too.
  fireEvent.change(input, { target: { value: "heavy squat" } });
  await waitFor(() => expect(options()).toHaveLength(1));
  expect(options()[0]).toContain("Squat");
});

test("choosing a clip opens its sheet in the library", async () => {
  const clip = apiAsset(1, { title: "Seated cable row", description: "Controlled rows with a pause.", facets: sampleFacets("movement:row") });
  installApi({ "GET /v1/me": ME, "GET /v1/projects": { items: [], next_cursor: null }, "GET /v1/assets": { items: [clip], next_cursor: null } });
  const { router, input } = await openPalette();
  fireEvent.change(input, { target: { value: "cable" } });
  await waitFor(() => expect(options()).toHaveLength(1));
  await act(async () => {
    fireEvent.click(screen.getAllByRole("option")[0] as HTMLElement);
  });
  await waitFor(() => expect(router.state.location.pathname).toBe("/library"));
  expect((router.state.location.search as { open?: string }).open).toBe(clip.id);
  expect(await screen.findByText("Controlled rows with a pause.")).toBeTruthy();
  expect(screen.getByRole("heading", { name: "Seated cable row" })).toBeTruthy();
});

test("the older pages are fetched while the palette is open, so older clips are found", async () => {
  installApi({
    "GET /v1/me": ME,
    "GET /v1/projects": { items: [], next_cursor: null },
    "GET /v1/assets": (call: { query: URLSearchParams }) =>
      call.query.get("cursor") ? { items: [apiAsset(2, { title: "Old squat clip" })], next_cursor: null } : { items: [apiAsset(1, { title: "New row clip" })], next_cursor: "page2" },
  });
  const { input } = await openPalette();
  fireEvent.change(input, { target: { value: "squat" } });
  await waitFor(() => expect(options()).toHaveLength(1));
  expect(options()[0]).toContain("Old squat clip");
  // Everything is loaded: the palette no longer says it searches only the recent ones.
  expect(screen.queryByTestId("palette-scope")).toBeNull();
});

test("when the older pages cannot be loaded the palette says it searches the recent ones only", async () => {
  installApi({
    "GET /v1/me": ME,
    "GET /v1/projects": { items: [], next_cursor: null },
    "GET /v1/assets": (call: { query: URLSearchParams }) => (call.query.get("cursor") ? apiError(500, "internal_error") : { items: [apiAsset(1, { title: "New row clip" })], next_cursor: "page2" }),
  });
  const { input } = await openPalette();
  fireEvent.change(input, { target: { value: "squat" } });
  expect((await screen.findByTestId("palette-scope")).textContent).toBe("Searching your recent projects and clips");
  await screen.findByText("No results for “squat” in your recent projects and clips.");
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
  const again = (await screen.findByPlaceholderText("Search your videos, clips and pages…")) as HTMLInputElement;
  fireEvent.change(again, { target: { value: "zzzz" } });
  await screen.findByText("No results for “zzzz”.");
});
