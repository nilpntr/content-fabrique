import type { Session } from "@/lib/auth-guard";
import { fetchSession } from "@/lib/session";

/** The part of Clerk's browser object the guard reads. */
export interface ClerkBrowser {
  loaded: boolean;
  user?: { id: string } | null;
  session?: { status?: string } | null;
  organization?: { id: string } | null;
}

/**
 * The session as the browser's Clerk object knows it: in memory, kept fresh by Clerk, with no request. A pending session
 * (a task such as choosing an organization is unfinished) counts as signed out, as it does on the server.
 */
export function sessionFromClerk(clerk: ClerkBrowser): Session {
  const signedIn = Boolean(clerk.user && clerk.session && clerk.session.status !== "pending");
  return { isAuthenticated: signedIn, userId: signedIn ? (clerk.user?.id ?? null) : null, orgId: signedIn ? (clerk.organization?.id ?? null) : null };
}

/** Waits until Clerk's browser object has loaded, then reads it. Null when it did not load in time. */
export async function readClientSession(timeoutMs = 5000, intervalMs = 25): Promise<Session | null> {
  const deadline = Date.now() + timeoutMs;
  for (;;) {
    const clerk = (window as unknown as { Clerk?: ClerkBrowser }).Clerk;
    if (clerk?.loaded) return sessionFromClerk(clerk);
    if (Date.now() >= deadline) return null;
    await new Promise((resolve) => setTimeout(resolve, intervalMs));
  }
}

/**
 * What the route guard decides from. On the server it is the request's verified session (so the first HTML of a signed-out
 * visitor is the redirect). In the browser, a navigation uses Clerk's in-memory state instead of asking the server again:
 * the server only sees the `__session` cookie, which lives 60 seconds and is refreshed in the background, so a stale
 * cookie would bounce a signed-in user. The API checks the token on every call whatever the browser believes. If Clerk has
 * not loaded in time, it falls back to the server.
 */
export async function resolveSession(): Promise<Session> {
  if (typeof window === "undefined") return fetchSession();
  return (await readClientSession()) ?? fetchSession();
}
