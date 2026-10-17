import { createMemoryHistory, RouterProvider } from "@tanstack/react-router";
import { createRoot } from "react-dom/client";
import { getRouter } from "@/router";
import { installWindowClerk } from "@/test-clerk";
import { FIXTURE_PROJECT_PATH, installFakeApi, scenarioOf } from "./fake-api";

/**
 * The fixtures viewer: the real app (router, shell, pages, hooks, client) on canned API answers, signed in with the tests' Clerk
 * stand-in. Open `/dev-fixtures/index.html?path=/projects/prj_...&theme=dark&credits=low&run=working`. Development only: the
 * check below stops it from running in a build, and vite.config.ts (the product's build) never includes this folder.
 */
if (!import.meta.env.DEV) throw new Error("The fixtures viewer is for development only.");

const params = new URLSearchParams(window.location.search);
const theme = params.get("theme");
if (theme === "dark" || theme === "light") localStorage.setItem("cf-theme", theme);

installWindowClerk();
installFakeApi(scenarioOf(params));

const router = getRouter(createMemoryHistory({ initialEntries: [params.get("path") ?? FIXTURE_PROJECT_PATH] }));
// The root route is the whole document, as when Start hydrates it.
createRoot(document).render(<RouterProvider router={router} />);
