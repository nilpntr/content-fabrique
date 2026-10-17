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

/** POST /v1/projects: the new-project flow's request. */
export type CreateProjectRequest = components["schemas"]["CreateProjectRequest"];
export type ProjectFormat = components["schemas"]["ProjectFormat"];
export type ProjectStatus = components["schemas"]["ProjectStatus"];
/** GET /v1/projects/{id}: a project with its footage and current timeline version. */
export type Project = components["schemas"]["Project"];
/** An entry of GET /v1/projects. */
export type ProjectSummary = components["schemas"]["ProjectSummary"];
export type ProjectsPage = components["schemas"]["ListProjectsResponse"];
/** One timeline version without its spec: number, author, the summary lines of what changed. */
export type TimelineVersionSummary = components["schemas"]["TimelineVersionSummary"];
/** One timeline version with its spec. */
export type TimelineVersion = components["schemas"]["TimelineVersion"];
export type StartEditRequest = components["schemas"]["StartEditRequest"];
export type RunProfile = components["schemas"]["RunProfile"];
export type RunStatus = components["schemas"]["RunStatus"];
export type RunEvent = components["schemas"]["RunEvent"];
/** An agent run, with its cost, budgets and a summary of its events. */
export type Run = components["schemas"]["Run"];

export type RenderProfile = components["schemas"]["RenderProfile"];
export type RenderStatus = components["schemas"]["RenderStatus"];
/** A render of a timeline version. In a list it has no presigned URLs (`files` is null): fetch it with GET /v1/renders/{id}. */
export type Render = components["schemas"]["Render"];

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

// ── Projects ────────────────────────────────────────────────────────────────

export const createProject = async (client: ApiClient, body: CreateProjectRequest): Promise<Project> => unwrap(await client.POST("/v1/projects", { body }));

/** One page of the workspace's projects (newest first). `cursor` is the previous page's `next_cursor`. */
export const fetchProjects = async (client: ApiClient, cursor?: string, signal?: AbortSignal): Promise<ProjectsPage> =>
  unwrap(await client.GET("/v1/projects", { params: { query: { limit: 50, cursor } }, signal }));

export const fetchProject = async (client: ApiClient, id: string, signal?: AbortSignal): Promise<Project> =>
  unwrap(await client.GET("/v1/projects/{id}", { params: { path: { id } }, signal }));

/** All versions of a project's timeline, newest first, without their specs. */
export const fetchTimelineVersions = async (client: ApiClient, id: string, signal?: AbortSignal): Promise<TimelineVersionSummary[]> => {
  const all: TimelineVersionSummary[] = [];
  let before: number | undefined;
  // 100 a page, so a second page is rare; the loop is bounded.
  for (let page = 0; page < 20; page++) {
    const data = unwrap(await client.GET("/v1/projects/{id}/timelines", { params: { path: { id }, query: { limit: 100, before } }, signal }));
    all.push(...(Array.isArray(data.items) ? data.items : []));
    if (typeof data.next_before !== "number") break;
    before = data.next_before;
  }
  return all;
};

export const fetchTimelineVersion = async (client: ApiClient, id: string, version: number, signal?: AbortSignal): Promise<TimelineVersion> =>
  unwrap(await client.GET("/v1/projects/{id}/timelines/{version}", { params: { path: { id, version } }, signal }));

// ── Runs ────────────────────────────────────────────────────────────────────

/** POST /v1/projects/{id}/edit: starts the editor agent. The answer is the run, `queued`. */
export const startEdit = async (client: ApiClient, id: string, body: StartEditRequest): Promise<Run> =>
  unwrap(await client.POST("/v1/projects/{id}/edit", { params: { path: { id } }, body }));

/** The run, with its events after `afterSeq`. */
export const fetchRun = async (client: ApiClient, id: string, afterSeq = 0, signal?: AbortSignal): Promise<Run> =>
  unwrap(await client.GET("/v1/runs/{id}", { params: { path: { id }, query: { after_seq: afterSeq, limit: RUN_EVENTS_PAGE } }, signal }));

/** How many events one GET /v1/runs/{id} asks for. */
export const RUN_EVENTS_PAGE = 200;

export const cancelRun = async (client: ApiClient, id: string): Promise<Run> => unwrap(await client.POST("/v1/runs/{id}/cancel", { params: { path: { id } } }));

export const replyToRun = async (client: ApiClient, id: string, text: string): Promise<Run> =>
  unwrap(await client.POST("/v1/runs/{id}/reply", { params: { path: { id } }, body: { text } }));

// ── Renders ─────────────────────────────────────────────────────────────────

/** The renders of a version, newest first. A list carries no URLs: fetch the one you want with {@link fetchRender}. */
export const fetchRenders = async (client: ApiClient, id: string, version: number, profile: RenderProfile, signal?: AbortSignal): Promise<Render[]> => {
  const data = unwrap(await client.GET("/v1/projects/{id}/renders", { params: { path: { id }, query: { version, profile, limit: 10 } }, signal }));
  return Array.isArray(data.items) ? data.items : [];
};

export const fetchRender = async (client: ApiClient, id: string, signal?: AbortSignal): Promise<Render> =>
  unwrap(await client.GET("/v1/renders/{id}", { params: { path: { id } }, signal }));

/** Queues a render (202), or returns the render that already waits, runs or is ready for that version and profile (200). */
export const createRender = async (client: ApiClient, id: string, version: number, profile: RenderProfile): Promise<Render> =>
  unwrap(await client.POST("/v1/projects/{id}/renders", { params: { path: { id } }, body: { version, profile } }));
