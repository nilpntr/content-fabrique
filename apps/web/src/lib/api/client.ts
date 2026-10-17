/**
 * The client of the Go API (P0-08, D-14): `openapi-fetch` over the types that `openapi-typescript` generated from
 * packages/schemas/openapi.yaml, so a path, a body or a response that the contract does not have fails to compile.
 *
 * Every call sends the Clerk session token as `Authorization: Bearer`; there are no cookies (the API is another origin and
 * allows none), so requests are made with `credentials: "omit"`. An error answer (`{ "error": { "code", "message" } }`) or
 * a failure to reach the API becomes an {@link ApiError}, so callers and TanStack Query see one error type.
 */
import type { components, paths } from "@cf/schemas/api";
import createClient from "openapi-fetch";

/** Returns a fresh session token, or null when nobody is signed in. Clerk's `useAuth().getToken`. */
export type GetToken = () => Promise<string | null>;

/** An error answer of the API (`{ "error": { "code", "message" } }`), or a failure to reach it (status 0). */
export class ApiError extends Error {
  readonly status: number;
  /** Stable, for programs: `no_active_workspace`, `invalid_token`, `network_error`... The server's are `ErrorCode`. */
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

// The types of the contract, under the names the app uses.
export type ErrorCode = components["schemas"]["ErrorCode"];
export type PlanName = components["schemas"]["PlanName"];
export type PlanStatus = components["schemas"]["PlanStatus"];
export type Entitlements = components["schemas"]["Entitlements"];
/** GET /v1/me. */
export type Me = components["schemas"]["MeResponse"];
/** GET /v1/workspace/plan. */
export type WorkspacePlan = components["schemas"]["WorkspacePlanResponse"];
/** GET /v1/library/summary. */
export type LibrarySummary = components["schemas"]["LibrarySummaryResponse"];
/** An asset of the library, as the API has it. */
export type ApiAsset = components["schemas"]["Asset"];
/** GET /v1/assets: a page of assets, newest first. */
export type AssetsPage = components["schemas"]["ListAssetsResponse"];

export interface ApiClientOptions {
  getToken: GetToken;
  /** For tests. Defaults to `apiBaseUrl()`. */
  baseUrl?: string;
  /** For tests. Defaults to the global `fetch`, looked up at call time. */
  fetch?: typeof fetch;
}

/** The typed client: `client.GET("/v1/me")`. Use {@link unwrap} on the result. */
export type ApiClient = ReturnType<typeof createApiClient>;

export function createApiClient(options: ApiClientOptions) {
  // openapi-fetch hands the finished Request to this function, which adds the token and turns a lost connection into an
  // ApiError. The token is fetched per request, so Clerk can hand out a fresh one each time.
  const authenticatedFetch = async (request: Request): Promise<Response> => {
    const token = await options.getToken();
    if (!token) throw new ApiError(401, "unauthenticated", "You are signed out.");
    request.headers.set("Authorization", `Bearer ${token}`);
    try {
      return await (options.fetch ?? fetch)(request);
    } catch (error) {
      if (error instanceof DOMException && error.name === "AbortError") throw error;
      throw new ApiError(0, "network_error", "The server could not be reached.");
    }
  };
  return createClient<paths>({
    baseUrl: options.baseUrl ?? apiBaseUrl(),
    fetch: authenticatedFetch,
    headers: { Accept: "application/json" },
    credentials: "omit",
    mode: "cors",
  });
}

/** What openapi-fetch returns for any call: the parsed body on success, or the parsed error body, and the response. */
interface Result<T> {
  data?: T;
  error?: unknown;
  response: Response;
}

/** The data of a successful call, or the {@link ApiError} of a failed one. */
export function unwrap<T>(result: Result<T>): T {
  if (result.error !== undefined || !result.response.ok) throw toApiError(result.response.status, result.error);
  return result.data as T;
}

function toApiError(status: number, body: unknown): ApiError {
  // The API's error shape; anything else (a proxy's error page, for example) is an unknown error of that status.
  if (typeof body === "object" && body !== null && "error" in body) {
    const { code, message } = (body as { error?: { code?: unknown; message?: unknown } }).error ?? {};
    if (typeof code === "string" && typeof message === "string") return new ApiError(status, code, message);
  }
  return new ApiError(status, "unknown_error", `The server answered ${status}.`);
}

export const fetchMe = async (client: ApiClient, signal?: AbortSignal): Promise<Me> => unwrap(await client.GET("/v1/me", { signal }));

export const fetchWorkspacePlan = async (client: ApiClient, signal?: AbortSignal): Promise<WorkspacePlan> =>
  unwrap(await client.GET("/v1/workspace/plan", { signal }));

export const fetchLibrarySummary = async (client: ApiClient, signal?: AbortSignal): Promise<LibrarySummary> =>
  unwrap(await client.GET("/v1/library/summary", { signal }));

/** One page of the workspace's assets (newest first). `cursor` is the previous page's `next_cursor`. */
export const fetchAssets = async (client: ApiClient, cursor?: string, signal?: AbortSignal): Promise<AssetsPage> =>
  unwrap(await client.GET("/v1/assets", { params: { query: { limit: 100, cursor } }, signal }));

/** Deletes an asset (aborting its upload if it is still in progress). */
export const deleteAsset = async (client: ApiClient, id: string): Promise<void> => {
  unwrap(await client.DELETE("/v1/assets/{id}", { params: { path: { id: id as ApiAsset["id"] } } }));
};

/** The hover-scrub track of a ready video as WebVTT text, every cue pointing at the sprite sheet through a fresh URL. It needs the bearer token, so it cannot be a plain `<track src>`. */
export const fetchSpriteVtt = async (client: ApiClient, id: string, signal?: AbortSignal): Promise<string> =>
  unwrap(await client.GET("/v1/assets/{id}/sprite.vtt", { params: { path: { id: id as ApiAsset["id"] } }, parseAs: "text", signal }));
