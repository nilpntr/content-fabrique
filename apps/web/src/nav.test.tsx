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

// The nav items point at /, /settings, /library, /projects and /projects/new; this tree adds children and a page outside the nav.
async function activeAt(path: string) {
  const rootRoute = createRootRoute({ component: Layout });
  const routes = ["/", "/library", "/settings", "/settings/$id", "/projects", "/projects/new", "/projects/$id", "/elsewhere"].map((to) =>
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
  expect(await activeAt("/settings")).toBe("Settings");
});

test("a trailing slash does not matter", async () => {
  expect(await activeAt("/settings/")).toBe("Settings");
});

test("a child route keeps its parent's item active", async () => {
  expect(await activeAt("/settings/42")).toBe("Settings");
});

test("/ is active only on /, not on every page", async () => {
  expect(await activeAt("/")).toBe("Home");
});

test("a page outside the nav has no active item", async () => {
  expect(await activeAt("/elsewhere")).toBe("none");
});

test("an unknown child path (a 404) activates nothing", async () => {
  expect(await activeAt("/settings/a/b")).toBe("none");
});

test("a lookalike path is not a child", async () => {
  expect(await activeAt("/settingsx")).toBe("none");
});

test("a nested nav item wins over its parent: /projects/new is New video", async () => {
  expect(await activeAt("/projects/new")).toBe("New video");
});

test("the parent of a nested nav item is active on its own page and on its children", async () => {
  expect(await activeAt("/projects")).toBe("Projects");
});

test("a project page (/projects/<id>) belongs to Projects", async () => {
  expect(await activeAt("/projects/prj_1")).toBe("Projects");
});

test("only pages that do something are in the nav", async () => {
  const { NAV_ITEMS } = await import("./nav");
  expect(NAV_ITEMS.map((item) => item.title)).toEqual(["Home", "Projects", "New video", "Library", "Settings"]);
});
