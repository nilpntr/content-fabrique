import { createServerFn } from "@tanstack/react-start";
import { getCookie } from "@tanstack/react-start/server";
import { parseSidebarOpen, SIDEBAR_COOKIE_NAME, sidebarOpenFromValue } from "@/lib/sidebar-state";

const readRequestCookie = createServerFn().handler(() => sidebarOpenFromValue(getCookie(SIDEBAR_COOKIE_NAME)));

/**
 * Whether the sidebar starts open, for the root route's loader.
 * During SSR it reads the request's cookie with `getCookie`, so the first paint is already right. If the loader
 * ever runs in the browser (a client-only render) it reads `document.cookie`, which avoids a round trip.
 */
export async function getSidebarOpen(): Promise<boolean> {
  return typeof document === "undefined" ? readRequestCookie() : parseSidebarOpen(document.cookie);
}
