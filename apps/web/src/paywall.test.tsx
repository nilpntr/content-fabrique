import { act, screen, within } from "@testing-library/react";
import { createMemoryHistory, RouterProvider } from "@tanstack/react-router";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, expect, test, vi } from "vitest";
import { getRouter } from "./router";
import type { Entitlements } from "@/lib/api";
import { ME } from "./test-clerk";

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

/** The API answers GET /v1/me with this workspace plan. */
function stubPlan(entitlements: Partial<Entitlements> | null) {
  const body = entitlements === null ? { ...ME, entitlements: undefined } : { ...ME, entitlements: { ...ME.entitlements, ...entitlements } };
  vi.stubGlobal(
    "fetch",
    vi.fn(async () => new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } })),
  );
}

const NO_PLAN = { plan: "none", status: "none", active: false, current_period_end: null } as const;

test("a workspace with an active plan gets the app, not the paywall", async () => {
  stubPlan({ active: true });
  renderAt("/reports");
  expect(await screen.findByRole("heading", { name: "Reports" })).toBeTruthy();
  expect(screen.queryByTestId("paywall")).toBeNull();
});

test("a workspace without a plan gets the paywall on every app route, inside the shell", async () => {
  stubPlan(NO_PLAN);
  for (const path of ["/", "/reports", "/projects/new", "/not-a-page"]) {
    renderAt(path);
    const paywall = await screen.findByTestId("paywall");
    // The page itself is not rendered behind it.
    expect(within(paywall).getByRole("heading", { level: 1, name: "Choose a plan to start" })).toBeTruthy();
    expect(screen.queryByRole("heading", { name: "Reports" })).toBeNull();
    expect(screen.queryByRole("heading", { name: "Overview" })).toBeNull();
    // The sidebar is still there, with the switchers, so the user can change workspace or account.
    expect(screen.getByRole("navigation", { name: "Main" })).toBeTruthy();
    expect(screen.getByTestId("clerk-organization-switcher")).toBeTruthy();
    expect(screen.getByTestId("clerk-user-button")).toBeTruthy();
    act(() => root?.unmount());
    root = undefined;
  }
});

test("the paywall shows the plans with their prices, and no checkout", async () => {
  stubPlan(NO_PLAN);
  renderAt("/");
  const paywall = await screen.findByTestId("paywall");
  const card = (id: string) => {
    const el = paywall.querySelector<HTMLElement>(`[data-plan="${id}"]`);
    if (!el) throw new Error(`no card for ${id}`);
    return within(el);
  };
  expect(card("trial").getByText("On request")).toBeTruthy();
  expect(card("trial").getByText("2 exported videos")).toBeTruthy();
  expect(card("studio").getByText("About €25 / month")).toBeTruthy();
  expect(card("studio").getByText(/own Anthropic key/)).toBeTruthy();
  expect(card("creator").getByText("About €59 / month")).toBeTruthy();
  expect(card("creator").getByText("15 videos a month")).toBeTruthy();
  expect(card("pro").getByText("About €129 / month")).toBeTruthy();
  expect(card("pro").getByText("Priority renders")).toBeTruthy();

  // "Contact us" until checkout exists: the workspace to activate, and no button or link that pays, no invented address.
  expect(within(paywall).getByRole("heading", { name: "Ask us to activate your plan" })).toBeTruthy();
  expect(within(paywall).getByText("Fit Studio")).toBeTruthy();
  expect(within(paywall).getByText(ME.workspace.id)).toBeTruthy();
  expect(within(paywall).queryByRole("button")).toBeNull();
  expect(within(paywall).queryByRole("link")).toBeNull();
  expect(paywall.querySelector('a[href^="mailto:"]')).toBeNull();
});

test("an expired plan says it has ended", async () => {
  stubPlan({ plan: "creator", status: "active", active: false, current_period_end: "2020-01-01T00:00:00Z" });
  renderAt("/");
  const paywall = await screen.findByTestId("paywall");
  expect(within(paywall).getByRole("heading", { level: 1, name: "Your plan has ended" })).toBeTruthy();
});

test("Settings stays reachable without a plan", async () => {
  stubPlan(NO_PLAN);
  renderAt("/settings");
  expect(await screen.findByRole("heading", { name: "Settings" })).toBeTruthy();
  expect(screen.queryByTestId("paywall")).toBeNull();
});

test("a response without entitlements is the paywall, never the app", async () => {
  stubPlan(null);
  renderAt("/");
  expect(await screen.findByTestId("paywall")).toBeTruthy();
  expect(screen.queryByRole("heading", { name: "Overview" })).toBeNull();
});

test("an unreachable API is an error with a retry, not a paywall and not the app", async () => {
  vi.stubGlobal("fetch", vi.fn().mockRejectedValue(new TypeError("Failed to fetch")));
  renderAt("/reports");
  const alert = await screen.findByRole("alert", {}, { timeout: 4000 });
  expect(within(alert).getByText("We could not check your plan")).toBeTruthy();
  expect(screen.queryByTestId("paywall")).toBeNull();
  expect(screen.queryByRole("heading", { name: "Reports" })).toBeNull();
});

test("a grant shows up on the next load: the same page, a different answer", async () => {
  stubPlan(NO_PLAN);
  renderAt("/reports");
  expect(await screen.findByTestId("paywall")).toBeTruthy();
  act(() => root?.unmount());
  root = undefined;

  stubPlan({ active: true }); // `cfctl plan grant`, then a reload
  renderAt("/reports");
  expect(await screen.findByRole("heading", { name: "Reports" })).toBeTruthy();
  expect(screen.queryByTestId("paywall")).toBeNull();
});
