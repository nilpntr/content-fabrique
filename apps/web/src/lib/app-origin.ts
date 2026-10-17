import { createServerFn } from "@tanstack/react-start";
import { getRequestUrl } from "@tanstack/react-start/server";

/**
 * The origin of this web app (APP_BASE_URL reduced to scheme://host[:port]), the one site Clerk's middleware accepts a
 * session token for (`authorizedParties`) and the only redirect target `ClerkProvider` is told about. The Go API checks the
 * same origin against the token's `azp`.
 *
 * Unset is an error, except on localhost (a developer machine, where the origin is the request's own): a silent default
 * anywhere else would accept tokens for the wrong site.
 */
export function appOriginFrom(appBaseUrl: string | undefined, request?: URL): string {
  const value = appBaseUrl?.trim();
  if (value) {
    const url = new URL(value);
    if (url.protocol !== "http:" && url.protocol !== "https:") throw new Error("APP_BASE_URL must be an http(s) URL.");
    return url.origin;
  }
  if (request && (request.hostname === "localhost" || request.hostname === "127.0.0.1")) return request.origin;
  throw new Error("APP_BASE_URL is not set (see .env.example).");
}

/** APP_BASE_URL in the Worker: a Worker variable (`vars`), handed over by `vite dev` locally. */
export function readAppBaseUrl(): string | undefined {
  return (globalThis as { process?: { env?: Record<string, string | undefined> } }).process?.env?.APP_BASE_URL;
}

const readServerAppOrigin = createServerFn().handler(() => appOriginFrom(readAppBaseUrl(), new URL(getRequestUrl())));

/**
 * For the root route's loader. On the server it is the configured origin. A browser-only render (tests) has no server to
 * ask and uses the page's own origin; the browser value is only an input to Clerk's redirect allowlist.
 */
export async function getAppOrigin(): Promise<string> {
  return typeof document === "undefined" ? readServerAppOrigin() : window.location.origin;
}
