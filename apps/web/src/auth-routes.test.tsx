import { act, screen, waitFor } from "@testing-library/react";
import { createMemoryHistory, RouterProvider } from "@tanstack/react-router";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, expect, test, vi } from "vitest";
import { getRouter } from "./router";
import { clerkMock } from "./test-clerk";

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

const appChrome = () => document.querySelector('[data-slot="sidebar"]');

test("a signed-out visitor gets the sign-in page, never the app", async () => {
  clerkMock.session = { isAuthenticated: false, userId: null, orgId: null };
  const router = renderAt("/");
  expect(await screen.findByTestId("clerk-sign-in")).toBeTruthy();
  expect(router.state.location.pathname).toBe("/sign-in");
  expect(appChrome()).toBeNull();
  expect(screen.queryByRole("navigation", { name: "Main" })).toBeNull();
  expect(screen.queryByRole("heading", { name: "Overview" })).toBeNull();
});

test("every app page is protected, and sign-in remembers where the visitor was going", async () => {
  clerkMock.session = { isAuthenticated: false, userId: null, orgId: null };
  for (const [path, redirectUrl] of [
    ["/settings", "/settings"],
    ["/reports", "/reports"],
    ["/not-a-page", "/not-a-page"],
  ] as const) {
    const router = renderAt(path);
    expect(await screen.findByTestId("clerk-sign-in")).toBeTruthy();
    expect(router.state.location.pathname).toBe("/sign-in");
    expect(new URLSearchParams(router.state.location.searchStr).get("redirect_url")).toBe(redirectUrl);
    expect(appChrome()).toBeNull();
    act(() => root?.unmount());
    root = undefined;
  }
});

test("the sign-in and sign-up pages are open and have no app chrome", async () => {
  clerkMock.session = { isAuthenticated: false, userId: null, orgId: null };
  renderAt("/sign-up");
  expect(await screen.findByTestId("clerk-sign-up")).toBeTruthy();
  expect(appChrome()).toBeNull();
  act(() => root?.unmount());
  root = undefined;

  // A step of Clerk's flow, handled by the catch-all.
  renderAt("/sign-in/tasks/choose-organization");
  expect(await screen.findByTestId("clerk-sign-in")).toBeTruthy();
});

test("a signed-in user without a workspace picks one before anything else", async () => {
  clerkMock.session = { isAuthenticated: true, userId: "user_test", orgId: null };
  const router = renderAt("/settings");
  expect(await screen.findByTestId("clerk-organization-list")).toBeTruthy();
  expect(router.state.location.pathname).toBe("/workspace");
  expect(appChrome()).toBeNull();
  // The API is not asked for anything either: there is no workspace to ask about.
  expect(fetch).not.toHaveBeenCalled();
});

test("a member with a workspace goes from the picker straight to the app", async () => {
  const router = renderAt("/workspace");
  expect(await screen.findByRole("heading", { name: "Overview" })).toBeTruthy();
  expect(router.state.location.pathname).toBe("/");
});

test("Settings shows what GET /v1/me returned, fetched with the session token", async () => {
  renderAt("/settings");
  expect(await screen.findByText("ada@example.com", { selector: "dd" })).toBeTruthy();
  expect(screen.getByText("usr_01test")).toBeTruthy();
  expect(screen.getByText("Fit Studio", { selector: "dd" })).toBeTruthy();
  expect(screen.getByText("wks_01test")).toBeTruthy();
  expect(screen.getByText("owner", { selector: "dd" })).toBeTruthy();

  const calls = vi.mocked(fetch).mock.calls;
  expect(calls.length).toBeGreaterThan(0);
  for (const [url, init] of calls) {
    expect(url).toBe("http://api.test/v1/me");
    expect(new Headers(init?.headers).get("Authorization")).toBe("Bearer test-token");
  }
});

test("Settings says so when the API refuses", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn(
      async () =>
        new Response(JSON.stringify({ error: { code: "workspace_deleted", message: "this workspace was deleted" } }), {
          status: 403,
          headers: { "Content-Type": "application/json" },
        }),
    ),
  );
  renderAt("/settings");
  const alert = await screen.findByRole("alert");
  await waitFor(() => expect(alert.textContent).toContain("this workspace was deleted (workspace_deleted)"));
});
