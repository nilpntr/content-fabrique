import { act, screen } from "@testing-library/react";
import { createMemoryHistory, createRootRoute, createRoute, createRouter, Outlet, RouterProvider } from "@tanstack/react-router";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, expect, test } from "vitest";
import { useActiveNavItem } from "./nav";

let root: Root | undefined;
afterEach(() => {
  act(() => root?.unmount());
  root = undefined;
  document.body.innerHTML = "";
});

function Active() {
  return <p data-testid="active">{useActiveNavItem()?.title ?? "none"}</p>;
}

function Layout() {
  return (
    <>
      <Active />
      <Outlet />
    </>
  );
}

// The nav items point at /, /reports and /settings; this tree adds a child of /reports and a page outside the nav.
async function activeAt(path: string) {
  const rootRoute = createRootRoute({ component: Layout });
  const routes = ["/", "/reports", "/settings", "/reports/$id", "/elsewhere"].map((to) =>
    createRoute({ getParentRoute: () => rootRoute, path: to, component: () => null }),
  );
  const router = createRouter({
    routeTree: rootRoute.addChildren(routes),
    history: createMemoryHistory({ initialEntries: [path] }),
    // Keep the path as given: the default would redirect a trailing slash away before the match.
    trailingSlash: "preserve",
  });
  root = createRoot(document.body.appendChild(document.createElement("div")));
  // biome-ignore lint/suspicious/noExplicitAny: the test tree is not the registered one.
  act(() => root?.render(<RouterProvider router={router as any} />));
  return (await screen.findByTestId("active")).textContent;
}

test("an exact path activates its item", async () => {
  expect(await activeAt("/reports")).toBe("Reports");
});

test("a trailing slash does not matter", async () => {
  expect(await activeAt("/reports/")).toBe("Reports");
});

test("a child route keeps its parent's item active", async () => {
  expect(await activeAt("/reports/42")).toBe("Reports");
});

test("/ is active only on /, not on every page", async () => {
  expect(await activeAt("/")).toBe("Overview");
});

test("a page outside the nav has no active item", async () => {
  expect(await activeAt("/elsewhere")).toBe("none");
});

test("a lookalike path is not a child", async () => {
  expect(await activeAt("/reportsx")).toBe("none");
});
