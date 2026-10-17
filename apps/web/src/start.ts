import { clerkMiddleware } from "@clerk/tanstack-react-start/server";
import { createCsrfMiddleware, createStart } from "@tanstack/react-start";
import { SIGN_IN_PATH, SIGN_UP_PATH } from "@/lib/auth-guard";

/**
 * The Start instance. Clerk's middleware runs on every request: it verifies the session cookie and makes `auth()`
 * work in server functions and loaders. A `src/start.ts` replaces Start's default request middleware, so the CSRF
 * check for server functions (same-origin only) is listed explicitly.
 */
export const startInstance = createStart(() => ({
  requestMiddleware: [
    createCsrfMiddleware({ filter: (ctx) => ctx.handlerType === "serverFn" }),
    clerkMiddleware({ signInUrl: SIGN_IN_PATH, signUpUrl: SIGN_UP_PATH }),
  ],
}));
