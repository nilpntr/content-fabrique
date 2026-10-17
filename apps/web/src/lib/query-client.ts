import { MutationCache, QueryCache, QueryClient } from "@tanstack/react-query";
import { ApiError } from "@/lib/api";
import { PLAN_REQUIRED_CODE } from "@/lib/paywall";

/** The query key prefix of GET /v1/me (see use-me.ts), which carries the entitlements the paywall is decided from. */
export const ME_QUERY_KEY = "me";
/** The query key prefix of GET /v1/workspace/plan (see lib/api/hooks.ts). */
export const PLAN_QUERY_KEY = "workspace-plan";
/** The query key prefix of GET /v1/library/summary (see lib/api/hooks.ts). */
export const LIBRARY_SUMMARY_QUERY_KEY = "library-summary";

/** The query key prefix of GET /v1/assets (see lib/api/hooks.ts). */
export const ASSETS_QUERY_KEY = "assets";

/** Whether an error is the API's 402: the workspace's plan lapsed (or was revoked) since `me` was fetched. */
export function isPlanRequired(error: unknown): boolean {
  return error instanceof ApiError && error.status === 402 && error.code === PLAN_REQUIRED_CODE;
}

/**
 * The query client of the app. Any call that the API refuses with 402 `plan_required` means the plan the page was
 * rendered with is out of date, so `me` (and the plan query) is fetched again and the paywall appears without a reload.
 */
export function createQueryClient(): QueryClient {
  const invalidatePlan = () => {
    void client.invalidateQueries({ queryKey: [ME_QUERY_KEY] });
    void client.invalidateQueries({ queryKey: [PLAN_QUERY_KEY] });
  };
  const client: QueryClient = new QueryClient({
    defaultOptions: {
      // Data fetched on the server is sent to the browser; a staleTime keeps it from being refetched at once.
      queries: { staleTime: 30_000 },
    },
    queryCache: new QueryCache({
      onError: (error) => {
        if (isPlanRequired(error)) invalidatePlan();
      },
    }),
    mutationCache: new MutationCache({
      onError: (error) => {
        if (isPlanRequired(error)) invalidatePlan();
      },
    }),
  });
  return client;
}
