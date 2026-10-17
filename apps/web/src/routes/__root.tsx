import { ClerkProvider } from "@clerk/tanstack-react-start";
import { createRootRoute, HeadContent, Outlet, redirect, Scripts, useRouterState } from "@tanstack/react-router";
import type { ReactNode } from "react";
import { APP_NAME, Wordmark } from "@/components/logo";
import { AppShell } from "@/components/app-shell";
import { ToastProvider } from "@/components/ui/toast";
import { TooltipProvider } from "@/components/ui/tooltip";
import { THEME_SCRIPT } from "@/hooks/use-theme";
import { decideAccess, isBarePath, SIGN_IN_PATH, SIGN_UP_PATH } from "@/lib/auth-guard";
import { getAppOrigin } from "@/lib/app-origin";
import { resolveSession } from "@/lib/client-session";
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
  // The route guard. Every route needs a signed-in user with an active workspace except the sign-in and sign-up pages
  // (src/lib/auth-guard.ts). On the server, for the first request, it runs before anything renders and reads the session
  // Clerk's middleware verified (src/start.ts), so a signed-out visitor is redirected and never receives app content.
  // In the browser, a navigation reads Clerk's in-memory state instead (src/lib/client-session.ts).
  beforeLoad: async ({ location }) => {
    const session = await resolveSession();
    const decision = decideAccess(location.pathname, session, location.searchStr);
    if (decision.kind === "redirect") throw redirect({ href: decision.to });
    return { session };
  },
  // The sidebar's state lives in a cookie, so the server can render it collapsed on the first paint.
  // Only the first render needs it (it is the sidebar's initial state), so it never reloads on navigation or preload.
  loader: async () => ({ sidebarOpen: await getSidebarOpen(), appOrigin: await getAppOrigin() }),
  staleTime: Number.POSITIVE_INFINITY,
  shouldReload: false,
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
  const { sidebarOpen, appOrigin } = Route.useLoaderData();
  const pathname = useRouterState({ select: (state) => state.location.pathname });
  return (
    // Clerk may send the browser only to this app's own origin (a redirect_url that names another site is ignored).
    <ClerkProvider
      signInUrl={SIGN_IN_PATH}
      signUpUrl={SIGN_UP_PATH}
      afterSignOutUrl={SIGN_IN_PATH}
      allowedRedirectOrigins={[appOrigin]}
    >
      <TooltipProvider>
        <ToastProvider>
          {isBarePath(pathname) ? <BareLayout /> : <AppShell defaultSidebarOpen={sidebarOpen} />}
        </ToastProvider>
      </TooltipProvider>
    </ClerkProvider>
  );
}

/** Sign-in, sign-up and the workspace picker: no sidebar, because there is no app to show yet. */
function BareLayout() {
  return (
    <main className="grid min-h-svh place-content-center justify-items-center gap-6 p-6">
      <Wordmark />
      <Outlet />
    </main>
  );
}
