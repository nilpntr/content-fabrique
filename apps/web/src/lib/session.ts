import { auth } from "@clerk/tanstack-react-start/server";
import { createServerFn } from "@tanstack/react-start";
import type { Session } from "@/lib/auth-guard";

/**
 * Reads the session on the server, from the request Clerk's middleware (src/start.ts) already verified. The route guard
 * calls it in `beforeLoad`, so the first HTML of a signed-out visitor is the redirect, never the app. A POST, so a
 * shared cache never serves one visitor's session to another.
 */
export const fetchSession = createServerFn({ method: "POST" }).handler(async (): Promise<Session> => {
  const state = await auth();
  return { isAuthenticated: state.isAuthenticated, userId: state.userId ?? null, orgId: state.orgId ?? null };
});
