import { act, fireEvent, screen, within } from "@testing-library/react";
import { createMemoryHistory, RouterProvider } from "@tanstack/react-router";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { getRouter } from "./router";

let root: Root | undefined;

afterEach(() => {
  act(() => root?.unmount());
  root = undefined;
});

// The root route is the whole document (<html> and up) and supplies the providers, so render it into
// `document` the way Start hydrates it.
function renderAt(path: string) {
  const router = getRouter(createMemoryHistory({ initialEntries: [path] }));
  root = createRoot(document);
  act(() => root?.render(<RouterProvider router={router} />));
  return router;
}

function removeStub(name: "animate" | "getAnimations" | "scrollTo") {
  Reflect.deleteProperty(Element.prototype, name);
}

test("renders the shell with the sidebar nav and the current page", async () => {
  renderAt("/reports");
  expect(await screen.findByRole("heading", { name: "Reports" })).toBeTruthy();
  expect(screen.getByText("Content Fabrique")).toBeTruthy();
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

test("the active item and the title come from the router's match, also with a trailing slash", async () => {
  renderAt("/reports/");
  const nav = within(await screen.findByRole("navigation", { name: "Main" }));
  expect(nav.getByRole("link", { name: "Reports" }).hasAttribute("data-active")).toBe(true);
  expect(document.querySelector('[data-slot="breadcrumb-page"]')?.textContent).toBe("Reports");
});

test("an unknown path under a nav page is Not found with no active item", async () => {
  renderAt("/reports/does-not-exist");
  const nav = within(await screen.findByRole("navigation", { name: "Main" }));
  expect(await screen.findByText("Nothing at this address")).toBeTruthy();
  expect(document.querySelector('[data-slot="breadcrumb-page"]')?.textContent).toBe("Not found");
  expect(nav.getAllByRole("link").some((link) => link.hasAttribute("data-active"))).toBe(false);
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

describe("the sidebar", () => {
  const sidebar = () => document.querySelector('[data-slot="sidebar"]');
  const state = () => sidebar()?.getAttribute("data-state");

  afterEach(() => {
    // biome-ignore lint/suspicious/noDocumentCookie: resets the cookie the sidebar writes.
    document.cookie = "sidebar_state=; path=/; max-age=0";
  });

  test("starts collapsed when the cookie says so", async () => {
    // biome-ignore lint/suspicious/noDocumentCookie: sets up the cookie the sidebar reads.
    document.cookie = "sidebar_state=false; path=/";
    renderAt("/reports");
    await screen.findByRole("heading", { name: "Reports" });
    expect(state()).toBe("collapsed");
  });

  test("starts open without a cookie", async () => {
    renderAt("/reports");
    await screen.findByRole("heading", { name: "Reports" });
    expect(state()).toBe("expanded");
  });

  test("Ctrl/Cmd+B toggles it and stores the state, but not while typing or with Alt", async () => {
    renderAt("/reports");
    await screen.findByRole("heading", { name: "Reports" });
    const field = document.body.appendChild(document.createElement("input"));

    fireEvent.keyDown(field, { key: "b", ctrlKey: true });
    expect(state()).toBe("expanded");
    fireEvent.keyDown(window, { key: "b", ctrlKey: true, altKey: true });
    expect(state()).toBe("expanded");

    fireEvent.keyDown(window, { key: "b", ctrlKey: true });
    expect(state()).toBe("collapsed");
    expect(document.cookie).toContain("sidebar_state=false");
    fireEvent.keyDown(window, { key: "b", metaKey: true });
    expect(state()).toBe("expanded");
    field.remove();
  });
});

describe("the page scroll", () => {
  beforeEach(() => {
    // jsdom has no Element.scrollTo; this one moves scrollTop like a browser does.
    Object.defineProperty(Element.prototype, "scrollTo", {
      configurable: true,
      value(this: Element, options: ScrollToOptions) {
        this.scrollTop = options.top ?? 0;
      },
    });
  });
  afterEach(() => removeStub("scrollTo"));

  test("a new page opens at the top of the scrolling <main>", async () => {
    const router = renderAt("/reports");
    await screen.findByRole("heading", { name: "Reports" });
    const main = document.querySelector<HTMLElement>('[data-slot="sidebar-inset"]');
    if (!main) throw new Error("no <main>");
    main.scrollTop = 300;
    await act(() => router.navigate({ to: "/settings" }));
    await screen.findByRole("heading", { name: "Settings" });
    expect(main.scrollTop).toBe(0);
  });
});

describe("the lens hand-off", () => {
  const animate = vi.fn(() => ({}));

  beforeEach(() => {
    animate.mockClear();
    Object.defineProperty(Element.prototype, "animate", { configurable: true, value: animate });
    Object.defineProperty(Element.prototype, "getAnimations", { configurable: true, value: () => [] });
    // Each nav item gets its own place, so the lens has somewhere to glide from.
    vi.spyOn(Element.prototype, "getBoundingClientRect").mockImplementation(function (this: Element) {
      const label = this.closest("a")?.textContent ?? "";
      const top = ["Overview", "Reports", "Settings"].indexOf(label) * 40;
      return { top, left: 0, width: 200, height: 36, right: 200, bottom: top + 36, x: 0, y: top, toJSON: () => ({}) };
    });
  });
  afterEach(() => {
    vi.restoreAllMocks();
    removeStub("animate");
    removeStub("getAnimations");
  });

  test("glides between items", async () => {
    const router = renderAt("/reports");
    await screen.findByRole("heading", { name: "Reports" });
    expect(animate).not.toHaveBeenCalled();
    await act(() => router.navigate({ to: "/settings" }));
    expect(animate).toHaveBeenCalled();
  });

  test("still glides after a stop on a page outside the nav", async () => {
    const router = renderAt("/reports");
    await screen.findByRole("heading", { name: "Reports" });
    await act(() => router.navigate({ to: "/nowhere" as never }));
    expect(await screen.findByText("Nothing at this address")).toBeTruthy();
    expect(animate).not.toHaveBeenCalled();
    await act(() => router.navigate({ to: "/settings" }));
    expect(animate).toHaveBeenCalled();
  });
});
