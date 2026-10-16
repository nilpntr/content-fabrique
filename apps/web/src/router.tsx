import { QueryClient } from "@tanstack/react-query";
import { createRootRoute, createRoute, createRouter } from "@tanstack/react-router";
import { AppShell } from "@/components/app-shell";
import { NotFound } from "@/pages/not-found";
import { OverviewPage } from "@/pages/overview";
import { ReportsPage } from "@/pages/reports";
import { SettingsPage } from "@/pages/settings";

export const queryClient = new QueryClient();

const rootRoute = createRootRoute({ component: AppShell, notFoundComponent: NotFound });

const overviewRoute = createRoute({ getParentRoute: () => rootRoute, path: "/", component: OverviewPage });
const reportsRoute = createRoute({ getParentRoute: () => rootRoute, path: "/reports", component: ReportsPage });
const settingsRoute = createRoute({ getParentRoute: () => rootRoute, path: "/settings", component: SettingsPage });

export const routeTree = rootRoute.addChildren([overviewRoute, reportsRoute, settingsRoute]);

export const router = createRouter({ routeTree, defaultPreload: "intent", scrollRestoration: true });

declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router;
  }
}
