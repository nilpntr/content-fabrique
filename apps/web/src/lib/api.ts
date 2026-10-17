/**
 * The client of the Go API. Every call sends the Clerk session token as `Authorization: Bearer`; there are no cookies
 * (the API is another origin and allows none), so the request is made with `credentials: "omit"`.
 *
 * Hand-written until P0-08 generates the client from the OpenAPI contract.
 */

/** Returns a fresh session token, or null when nobody is signed in. Clerk's `useAuth().getToken`. */
export type GetToken = () => Promise<string | null>;

/** An error answer of the API (`{ "error": { "code", "message" } }`), or a failure to reach it (status 0). */
export class ApiError extends Error {
  readonly status: number;
  /** Stable, for programs: `no_active_workspace`, `invalid_token`, `network_error`... */
  readonly code: string;

  constructor(status: number, code: string, message: string) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.code = code;
  }
}

/** The API's base URL (`VITE_API_BASE_URL`, public). A missing value is a setup error and says so. */
export function apiBaseUrl(): string {
  const url = import.meta.env.VITE_API_BASE_URL as string | undefined;
  if (!url) throw new ApiError(0, "not_configured", "VITE_API_BASE_URL is not set (see .env.example).");
  return url.replace(/\/+$/, "");
}

export interface RequestOptions {
  getToken: GetToken;
  method?: "GET" | "POST" | "PUT" | "PATCH" | "DELETE";
  body?: unknown;
  signal?: AbortSignal;
  /** For tests. */
  baseUrl?: string;
  fetch?: typeof fetch;
}

export async function apiRequest<T>(path: string, options: RequestOptions): Promise<T> {
  const token = await options.getToken();
  if (!token) throw new ApiError(401, "unauthenticated", "You are signed out.");

  const headers = new Headers({ Authorization: `Bearer ${token}`, Accept: "application/json" });
  if (options.body !== undefined) headers.set("Content-Type", "application/json");

  let response: Response;
  try {
    response = await (options.fetch ?? fetch)(`${options.baseUrl ?? apiBaseUrl()}${path}`, {
      method: options.method ?? "GET",
      headers,
      body: options.body === undefined ? undefined : JSON.stringify(options.body),
      credentials: "omit",
      mode: "cors",
      signal: options.signal,
    });
  } catch (error) {
    if (error instanceof DOMException && error.name === "AbortError") throw error;
    throw new ApiError(0, "network_error", "The server could not be reached.");
  }

  if (!response.ok) throw await toApiError(response);
  return (await response.json()) as T;
}

async function toApiError(response: Response): Promise<ApiError> {
  try {
    const body = (await response.json()) as { error?: { code?: unknown; message?: unknown } };
    const { code, message } = body.error ?? {};
    if (typeof code === "string" && typeof message === "string") return new ApiError(response.status, code, message);
  } catch {
    // Not the API's error shape (a proxy's error page, for example).
  }
  return new ApiError(response.status, "unknown_error", `The server answered ${response.status}.`);
}

/** The plans, as the API names them. `none` is no plan: the paywall. */
export type PlanName = "none" | "trial" | "studio" | "creator" | "pro";

/** The workspace's plan (P0-16). Only `active` decides what the app shows; the rest is for display. */
export interface Entitlements {
  plan: PlanName;
  /** `none` when the workspace never had a plan. */
  status: "none" | "active" | "past_due" | "canceled";
  active: boolean;
  /** ISO time when the paid period ends, or null when it does not. */
  current_period_end: string | null;
}

/** GET /v1/me. */
export interface Me {
  user: { id: string; email: string };
  workspace: { id: string; name: string; role: "owner" | "editor" | "viewer" };
  entitlements: Entitlements;
}

export function fetchMe(options: Omit<RequestOptions, "method" | "body">): Promise<Me> {
  return apiRequest<Me>("/v1/me", options);
}
