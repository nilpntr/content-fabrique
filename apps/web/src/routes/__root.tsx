import { createRootRoute, HeadContent, Scripts } from "@tanstack/react-router";
import type { ReactNode } from "react";
import { APP_NAME } from "@/components/logo";
import { AppShell } from "@/components/app-shell";
import { ToastProvider } from "@/components/ui/toast";
import { TooltipProvider } from "@/components/ui/tooltip";
import { THEME_SCRIPT } from "@/hooks/use-theme";
import { getSidebarOpen } from "@/lib/sidebar-open";
import { NotFound } from "@/pages/not-found";
import appCss from "../index.css?url";

const FAVICON =
  "data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 32 32' fill='none'%3E%3Crect width='32' height='32' rx='9.5' fill='%230b0b0b'/%3E%3Crect x='8.5' y='8.5' width='6.5' height='6.5' rx='2' fill='%2383b0ff'/%3E%3Crect x='17' y='8.5' width='6.5' height='6.5' rx='2' fill='%23fcfcfb' fill-opacity='0.9'/%3E%3Crect x='8.5' y='17' width='6.5' height='6.5' rx='2' fill='%23fcfcfb' fill-opacity='0.5'/%3E%3Crect x='17' y='17' width='6.5' height='6.5' rx='2' fill='%23fcfcfb' fill-opacity='0.5'/%3E%3C/svg%3E";

export const Route = createRootRoute({
  head: () => ({
    meta: [
      { charSet: "utf-8" },
      { name: "viewport", content: "width=device-width, initial-scale=1.0" },
      { name: "color-scheme", content: "light dark" },
      { title: APP_NAME },
    ],
    links: [
      { rel: "icon", type: "image/svg+xml", href: FAVICON },
      { rel: "stylesheet", href: appCss },
    ],
    // Sets the `dark` class before first paint, so a dark-mode visitor never sees a light flash.
    scripts: [{ children: THEME_SCRIPT }],
  }),
  // The sidebar's state lives in a cookie, so the server can render it collapsed on the first paint.
  loader: async () => ({ sidebarOpen: await getSidebarOpen() }),
  shellComponent: RootDocument,
  component: RootComponent,
  notFoundComponent: NotFound,
});

function RootDocument({ children }: { children: ReactNode }) {
  return (
    // The inline script changes the class on <html> before React hydrates it.
    <html lang="en" suppressHydrationWarning>
      <head>
        <HeadContent />
      </head>
      <body>
        {children}
        <Scripts />
      </body>
    </html>
  );
}

function RootComponent() {
  const { sidebarOpen } = Route.useLoaderData();
  return (
    <TooltipProvider>
      <ToastProvider>
        <AppShell defaultSidebarOpen={sidebarOpen} />
      </ToastProvider>
    </TooltipProvider>
  );
}
