import { MutationCache, QueryCache, QueryClient } from "@tanstack/react-query";
import { ApiError } from "@/lib/api";
import { announceCreditsExhausted, isCreditsExhausted } from "@/lib/credits";
import { PLAN_REQUIRED_CODE } from "@/lib/paywall";

/** The query key prefix of GET /v1/me (see use-me.ts), which carries the entitlements the paywall is decided from. */
export const ME_QUERY_KEY = "me";
/** The query key prefix of GET /v1/workspace/plan (see lib/api/hooks.ts). */
export const PLAN_QUERY_KEY = "workspace-plan";
/** The query key prefix of GET /v1/library/summary (see lib/api/hooks.ts). */
export const LIBRARY_SUMMARY_QUERY_KEY = "library-summary";

/** The query key prefix of GET /v1/assets (see lib/api/hooks.ts). */
export const ASSETS_QUERY_KEY = "assets";

// The keys of the projects hooks (lib/api/hooks.ts). Each is followed by the Clerk user and organization, then by the ids.
/** The list of projects: `[PROJECTS_QUERY_KEY, user, org]`. */
export const PROJECTS_QUERY_KEY = "projects";
/** One project: `[PROJECT_QUERY_KEY, user, org, projectId]`. */
export const PROJECT_QUERY_KEY = "project";
/** The versions of a project's timeline: `[TIMELINES_QUERY_KEY, user, org, projectId]`, and `[..., projectId, version]` for one with its spec. */
export const TIMELINES_QUERY_KEY = "timelines";
/** One agent run: `[RUN_QUERY_KEY, user, org, runId]`. */
export const RUN_QUERY_KEY = "run";
/** The artifacts of one run (its plan, self-review, previews, versions): `[RUN_ARTIFACTS_QUERY_KEY, user, org, runId]`. */
export const RUN_ARTIFACTS_QUERY_KEY = "run-artifacts";
/** The preview render of a version: `[RENDERS_QUERY_KEY, user, org, projectId, version]` (the list), `[RENDER_QUERY_KEY, ..., renderId]` (one). */
export const RENDERS_QUERY_KEY = "renders";
export const RENDER_QUERY_KEY = "render";
/** The comments of a project: `[ANNOTATIONS_QUERY_KEY, user, org, projectId]`. */
export const ANNOTATIONS_QUERY_KEY = "annotations";

/** Whether an error is the API's 402: the workspace's plan lapsed (or was revoked) since `me` was fetched. */
export function isPlanRequired(error: unknown): boolean {
  return error instanceof ApiError && error.status === 402 && error.code === PLAN_REQUIRED_CODE;
}

/**
 * The query client of the app. Any call that the API refuses with 402 `plan_required` means the plan the page was
 * rendered with is out of date, so `me` (and the plan query) is fetched again and the paywall appears without a reload.
 * Any call refused with 402 `credits_exhausted` opens the "out of credits" dialog (by its code, so a new call that spends
 * credits needs no code of its own) and fetches `me` again, which has the balance.
 */
export function createQueryClient(): QueryClient {
  const invalidatePlan = () => {
    void client.invalidateQueries({ queryKey: [ME_QUERY_KEY] });
    void client.invalidateQueries({ queryKey: [PLAN_QUERY_KEY] });
  };
  const onError = (error: unknown) => {
    if (isPlanRequired(error)) invalidatePlan();
    if (isCreditsExhausted(error)) {
      announceCreditsExhausted();
      void client.invalidateQueries({ queryKey: [ME_QUERY_KEY] });
    }
  };
  const client: QueryClient = new QueryClient({
    defaultOptions: {
      // Data fetched on the server is sent to the browser; a staleTime keeps it from being refetched at once.
      queries: { staleTime: 30_000 },
    },
    queryCache: new QueryCache({ onError }),
    mutationCache: new MutationCache({ onError }),
  });
  return client;
}
