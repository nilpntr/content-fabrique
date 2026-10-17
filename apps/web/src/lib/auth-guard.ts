/**
 * Who may see which route. Pure logic, so it is tested without a router or Clerk.
 *
 * The rule is "protected unless listed": every route needs a signed-in user with an active workspace (a Clerk
 * organization), and only the sign-in and sign-up pages are open. A new route is therefore protected without anyone
 * remembering to do it. The server enforces it (the root route's `beforeLoad` runs `auth()` on the server before
 * anything renders), so a signed-out request never gets app content in the HTML. The API checks the token again on
 * every call, so this guard is about what the browser is shown, not about data.
 */

export const SIGN_IN_PATH = "/sign-in";
export const SIGN_UP_PATH = "/sign-up";
/** Where a signed-in user without an active workspace picks or creates one. */
export const WORKSPACE_PATH = "/workspace";

/** What the server knows about the request's session. */
export interface Session {
  isAuthenticated: boolean;
  userId: string | null;
  /** The active Clerk organization, i.e. the workspace. Null when the user has not picked one. */
  orgId: string | null;
}

export type AccessDecision = { kind: "allow" } | { kind: "redirect"; to: string };

function firstSegment(pathname: string): string {
  return (pathname.split("/")[1] ?? "").toLowerCase();
}

/** `/sign-in` and everything under it (Clerk's multi-step flows and tasks live at `/sign-in/...`), likewise sign-up. */
export function isAuthPath(pathname: string): boolean {
  const segment = firstSegment(pathname);
  return segment === "sign-in" || segment === "sign-up";
}

export function isWorkspacePath(pathname: string): boolean {
  return firstSegment(pathname) === "workspace";
}

/** The sign-in URL that brings the visitor back to where they were going. Only a path on this site is ever sent. */
export function signInHref(pathname: string, search = ""): string {
  const target = `${pathname}${search}`;
  if (!target.startsWith("/") || target.startsWith("//") || isAuthPath(pathname) || target === "/") return SIGN_IN_PATH;
  return `${SIGN_IN_PATH}?redirect_url=${encodeURIComponent(target)}`;
}

export function decideAccess(pathname: string, session: Session, search = ""): AccessDecision {
  // Clerk's own components decide what a visitor on these pages sees (including a session that still has a task, such
  // as choosing an organization, to finish).
  if (isAuthPath(pathname)) return { kind: "allow" };
  if (!session.isAuthenticated) return { kind: "redirect", to: signInHref(pathname, search) };
  if (!session.orgId) return isWorkspacePath(pathname) ? { kind: "allow" } : { kind: "redirect", to: WORKSPACE_PATH };
  if (isWorkspacePath(pathname)) return { kind: "redirect", to: "/" };
  return { kind: "allow" };
}

/** Pages that are not part of the app shell: no sidebar, centred on the page. */
export function isBarePath(pathname: string): boolean {
  return isAuthPath(pathname) || isWorkspacePath(pathname);
}
