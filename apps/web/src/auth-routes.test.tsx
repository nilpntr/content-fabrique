import { act, screen, waitFor, within } from "@testing-library/react";
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
  expect(screen.queryByRole("heading", { name: /^Good (morning|afternoon|evening)/ })).toBeNull();
});

test("every app page is protected, and sign-in remembers where the visitor was going", async () => {
  clerkMock.session = { isAuthenticated: false, userId: null, orgId: null };
  for (const [path, redirectUrl] of [
    ["/settings", "/settings"],
    ["/library", "/library"],
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
  expect(await screen.findByRole("heading", { level: 1, name: /^Good (morning|afternoon|evening)/ })).toBeTruthy();
  expect(router.state.location.pathname).toBe("/");
});

test("Settings shows what GET /v1/me returned, fetched with the session token", async () => {
  renderAt("/settings");
  expect(await screen.findByText("ada@example.com", { selector: "dd" })).toBeTruthy();
  expect(screen.getByText("usr_01test")).toBeTruthy();
  expect(screen.getByText("Fit Studio", { selector: "dd" })).toBeTruthy();
  expect(screen.getByText("wks_01test")).toBeTruthy();
  expect(screen.getByText("Owner", { selector: "dd" })).toBeTruthy();
  // The ids are there for support, behind a closed "Details".
  expect(screen.getByText("usr_01test").closest("details")?.open).toBe(false);

  const calls = vi.mocked(fetch).mock.calls;
  expect(calls.length).toBeGreaterThan(0);
  for (const [request] of calls) {
    // The typed client calls fetch with one Request.
    expect(request).toBeInstanceOf(Request);
    expect((request as Request).url).toBe("http://api.test/v1/me");
    expect((request as Request).headers.get("Authorization")).toBe("Bearer test-token");
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
  await waitFor(() => expect(alert.textContent).toContain("this workspace was deleted"));
  // The code is only in the details.
  expect(within(alert).getByText("workspace_deleted").closest("details")?.open).toBe(false);
});

test("navigating in the browser uses Clerk's in-memory state and does not ask the server again", async () => {
  const router = renderAt("/library");
  await screen.findByRole("heading", { name: "Library" });
  await act(() => router.navigate({ to: "/settings" }));
  expect(await screen.findByRole("heading", { name: "Settings" })).toBeTruthy();
  expect(clerkMock.serverSessionCalls).toBe(0);
});

test("a stale session cookie on the server does not bounce a user Clerk knows is signed in", async () => {
  // The server only sees the 60-second __session cookie, which can be out of date while Clerk's own state is current.
  clerkMock.serverSession = { isAuthenticated: false, userId: null, orgId: null };
  const router = renderAt("/library");
  await screen.findByRole("heading", { name: "Library" });
  await act(() => router.navigate({ to: "/settings" }));
  expect(await screen.findByRole("heading", { name: "Settings" })).toBeTruthy();
  expect(router.state.location.pathname).toBe("/settings");
});

test("signing out in the browser sends the next navigation to sign-in", async () => {
  const router = renderAt("/library");
  await screen.findByRole("heading", { name: "Library" });
  clerkMock.session = { isAuthenticated: false, userId: null, orgId: null };
  await act(() => router.navigate({ to: "/settings" }));
  expect(await screen.findByTestId("clerk-sign-in")).toBeTruthy();
  expect(router.state.location.pathname).toBe("/sign-in");
});

test("Settings keeps retrying while the API answers 503, and then shows the data", async () => {
  const ok = new Response(JSON.stringify({ user: { id: "usr_1", email: "ada@example.com" }, workspace: { id: "wks_1", name: "Fit", role: "owner" } }), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });
  const unavailable = () =>
    new Response(JSON.stringify({ error: { code: "identity_unavailable", message: "try again" } }), { status: 503 });
  vi.stubGlobal("fetch", vi.fn().mockResolvedValueOnce(unavailable()).mockResolvedValue(ok));
  renderAt("/settings");
  expect(await screen.findByText("ada@example.com", { selector: "dd" }, { timeout: 5000 })).toBeTruthy();
  expect(vi.mocked(fetch).mock.calls.length).toBeGreaterThanOrEqual(2);
});
