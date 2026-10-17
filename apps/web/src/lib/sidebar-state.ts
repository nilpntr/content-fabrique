export const SIDEBAR_COOKIE_NAME = "sidebar_state";
export const SIDEBAR_COOKIE_MAX_AGE = 60 * 60 * 24 * 7;

/**
 * Whether the sidebar is open, from a `Cookie` header (or `document.cookie`).
 * Anything but an explicit `false` means open, so a missing, malformed or tampered cookie gives the default.
 */
export function parseSidebarOpen(cookieHeader: string | null | undefined): boolean {
  for (const part of (cookieHeader ?? "").split(";")) {
    const separator = part.indexOf("=");
    if (separator === -1) continue;
    if (part.slice(0, separator).trim() === SIDEBAR_COOKIE_NAME) return part.slice(separator + 1).trim() !== "false";
  }
  return true;
}

export function sidebarCookie(open: boolean): string {
  return `${SIDEBAR_COOKIE_NAME}=${open}; path=/; max-age=${SIDEBAR_COOKIE_MAX_AGE}; samesite=lax`;
}
