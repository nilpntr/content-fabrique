import { QueryClient } from "@tanstack/react-query";
import { createRouter, type RouterHistory } from "@tanstack/react-router";
import { setupRouterSsrQueryIntegration } from "@tanstack/react-router-ssr-query";
import { routeTree } from "./routeTree.gen";

/**
 * Start calls this once per server request and once in the browser, so nothing is shared between visitors.
 * Tests pass a memory history.
 */
export function getRouter(history?: RouterHistory) {
  // Data fetched on the server is sent to the browser; a staleTime keeps it from being refetched at once.
  const queryClient = new QueryClient({ defaultOptions: { queries: { staleTime: 30_000 } } });
  const router = createRouter({
    routeTree,
    history,
    defaultPreload: "intent",
    scrollRestoration: true,
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
