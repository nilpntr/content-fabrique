import { useAuth } from "@clerk/tanstack-react-start";
import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useRef } from "react";
import { type ApiAsset, type ApiClient, ApiError, deleteAsset, fetchAssets, fetchLibrarySummary, fetchMe, fetchWorkspacePlan } from "@/lib/api/client";
import { useApiClient } from "@/lib/api/provider";
import { ASSETS_QUERY_KEY, LIBRARY_SUMMARY_QUERY_KEY, ME_QUERY_KEY, PLAN_QUERY_KEY } from "@/lib/query-client";

/**
 * The TanStack Query hooks over the typed client (the one that ApiClientProvider provides). Every key holds the Clerk user
 * and organization, so switching workspace fetches again and never shows the previous workspace's data. They run in the
 * browser only (the token comes from Clerk's browser session), so server rendering shows the loading state. A 402
 * `plan_required` from any of them fetches `me` again (see query-client.ts), which is how the paywall appears without a reload.
 */

/** A 4xx is an answer (no workspace, deleted account, no plan), so asking again will not change it; a 5xx or a lost connection is worth a couple more tries. */
function retryUnlessClientError(failureCount: number, error: unknown): boolean {
  return failureCount < 2 && !(error instanceof ApiError && error.status >= 400 && error.status < 500);
}

function useWorkspaceQuery<T>(key: string, fetcher: (client: ApiClient, signal: AbortSignal) => Promise<T>) {
  const { isLoaded, isSignedIn, userId, orgId } = useAuth();
  const getClient = useApiClient();
  return useQuery({
    queryKey: [key, userId, orgId],
    queryFn: ({ signal }) => fetcher(getClient(), signal),
    enabled: isLoaded && Boolean(isSignedIn) && Boolean(orgId),
    retry: retryUnlessClientError,
  });
}

/** The signed-in user and active workspace, with the plan, from GET /v1/me. */
export function useMe() {
  return useWorkspaceQuery(ME_QUERY_KEY, fetchMe);
}

/** The plan of the active workspace and what it includes, from GET /v1/workspace/plan. */
export function useWorkspacePlan() {
  return useWorkspaceQuery(PLAN_QUERY_KEY, fetchWorkspacePlan);
}

/** The counts of the workspace's library, from GET /v1/library/summary (paid-only: 402 without an active plan). */
export function useLibrarySummary() {
  return useWorkspaceQuery(LIBRARY_SUMMARY_QUERY_KEY, fetchLibrarySummary);
}

/**
 * The list is fetched again while the server works on assets that this browser is not telling about itself: `uploaded`
 * (ingest queued) and `processing`. An asset that this browser is uploading is skipped (the upload manager reports its
 * progress and tells the list when it starts and finishes), and so is an `uploading` one of another device. The interval
 * starts at 3 s and doubles, up to 30 s, for as long as a poll finds nothing new.
 */
const POLLED_STATUSES: ReadonlySet<string> = new Set(["uploaded", "processing"]);
const POLL_MIN_MS = 3_000;
const POLL_MAX_MS = 30_000;

/** The assets worth polling for, as a string that changes when one of them does. */
export function pollSignature(assets: readonly ApiAsset[], isOwned: (id: string) => boolean): string {
  return assets
    .filter((asset) => POLLED_STATUSES.has(asset.status) && !isOwned(asset.id))
    .map((asset) => `${asset.id}:${asset.status}:${asset.updated_at}`)
    .join(",");
}

/** How long to wait before the next poll: `unchanged` is how many polls in a row found nothing new. */
export function pollDelay(unchanged: number): number {
  return Math.min(POLL_MAX_MS, POLL_MIN_MS * 2 ** Math.min(unchanged, 10));
}

/**
 * The workspace's assets from GET /v1/assets, newest first, all pages fetched on demand (`fetchNextPage`). Each page's
 * `items` is read defensively: an answer that is not a page counts as no assets. A clip that is being processed turns
 * `ready` without a reload (see {@link pollDelay}).
 */
export function useAssets({ enabled = true, isOwned = () => false }: { enabled?: boolean; isOwned?: (id: string) => boolean } = {}) {
  const { isLoaded, isSignedIn, userId, orgId } = useAuth();
  const getClient = useApiClient();
  const owned = useRef(isOwned);
  owned.current = isOwned;
  const polls = useRef({ signature: "", unchanged: 0 });
  return useInfiniteQuery({
    queryKey: [ASSETS_QUERY_KEY, userId, orgId],
    queryFn: ({ pageParam, signal }) => fetchAssets(getClient(), pageParam, signal),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (page) => (typeof page.next_cursor === "string" ? page.next_cursor : undefined),
    select: (data): ApiAsset[] => data.pages.flatMap((page) => (Array.isArray(page.items) ? page.items : [])),
    enabled: enabled && isLoaded && Boolean(isSignedIn) && Boolean(orgId),
    retry: retryUnlessClientError,
    refetchInterval: (query) => {
      const items = (query.state.data?.pages ?? []).flatMap((page) => (Array.isArray(page.items) ? page.items : []));
      const signature = pollSignature(items, owned.current);
      if (signature === "") {
        polls.current = { signature: "", unchanged: 0 };
        return false;
      }
      polls.current = { signature, unchanged: signature === polls.current.signature ? polls.current.unchanged + 1 : 0 };
      return pollDelay(polls.current.unchanged);
    },
  });
}

/** DELETE /v1/assets/{id}, then the list is fetched again. */
export function useDeleteAsset() {
  const getClient = useApiClient();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => deleteAsset(getClient(), id),
    onSettled: () => queryClient.invalidateQueries({ queryKey: [ASSETS_QUERY_KEY] }),
  });
}
