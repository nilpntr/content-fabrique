import { createRouter, type RouterHistory } from "@tanstack/react-router";
import { setupRouterSsrQueryIntegration } from "@tanstack/react-router-ssr-query";
import { createQueryClient } from "./lib/query-client";
import { MAIN_SCROLL_ID } from "./lib/scroll";
import { routeTree } from "./routeTree.gen";

/**
 * Start calls this once per server request and once in the browser, so nothing is shared between visitors.
 * Tests pass a memory history.
 */
export function getRouter(history?: RouterHistory) {
  const queryClient = createQueryClient();
  const router = createRouter({
    routeTree,
    history,
    defaultPreload: "intent",
    // Pages scroll inside the shell's <main>, not the window, so it is reset on navigation too.
    scrollRestoration: true,
    scrollToTopSelectors: [`[data-scroll-restoration-id="${MAIN_SCROLL_ID}"]`],
  });
  // Also wraps the router in a QueryClientProvider.
  setupRouterSsrQueryIntegration({ router, queryClient });
  return router;
}

declare module "@tanstack/react-router" {
  interface Register {
    router: ReturnType<typeof getRouter>;
  }
}
