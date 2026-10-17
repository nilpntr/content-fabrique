import { MutationCache, QueryCache, QueryClient } from "@tanstack/react-query";
import { ApiError } from "@/lib/api";
import { PLAN_REQUIRED_CODE } from "@/lib/paywall";

/** The query key prefix of GET /v1/me (see use-me.ts), which carries the entitlements the paywall is decided from. */
export const ME_QUERY_KEY = "me";

/** Whether an error is the API's 402: the workspace's plan lapsed (or was revoked) since `me` was fetched. */
export function isPlanRequired(error: unknown): boolean {
  return error instanceof ApiError && error.status === 402 && error.code === PLAN_REQUIRED_CODE;
}

/**
 * The query client of the app. Any call that the API refuses with 402 `plan_required` means the plan the page was
 * rendered with is out of date, so `me` is fetched again and the paywall appears without a reload.
 */
export function createQueryClient(): QueryClient {
  const client: QueryClient = new QueryClient({
    defaultOptions: {
      // Data fetched on the server is sent to the browser; a staleTime keeps it from being refetched at once.
      queries: { staleTime: 30_000 },
    },
    queryCache: new QueryCache({
      onError: (error) => {
        if (isPlanRequired(error)) void client.invalidateQueries({ queryKey: [ME_QUERY_KEY] });
      },
    }),
    mutationCache: new MutationCache({
      onError: (error) => {
        if (isPlanRequired(error)) void client.invalidateQueries({ queryKey: [ME_QUERY_KEY] });
      },
    }),
  });
  return client;
}
