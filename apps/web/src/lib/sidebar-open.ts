import { createServerFn } from "@tanstack/react-start";
import { getRequestHeader } from "@tanstack/react-start/server";
import { parseSidebarOpen } from "@/lib/sidebar-state";

const readRequestCookie = createServerFn().handler(() => parseSidebarOpen(getRequestHeader("cookie")));

/**
 * Whether the sidebar starts open, for the root route's loader.
 * During SSR it reads the request's cookie, so the first paint is already right. In the browser (client
 * navigations re-run the loader) it reads `document.cookie`, which avoids a round trip.
 */
export async function getSidebarOpen(): Promise<boolean> {
  return typeof document === "undefined" ? readRequestCookie() : parseSidebarOpen(document.cookie);
}
