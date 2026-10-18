import { useAuth } from "@clerk/tanstack-react-start";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { type CreatorStyle, fetchStyle, putStyleSettings, relearnStyle, removeStyleReference, resetStyle, type StyleSettings } from "@/lib/api/client";
import { retryUnlessClientError } from "@/lib/api/hooks";
import { useApiClient } from "@/lib/api/provider";
import { ASSETS_QUERY_KEY, STYLE_QUERY_KEY } from "@/lib/query-client";

/** How often the style is fetched again while it is being learnt. */
export const STYLE_POLL_MS = 4_000;

/** Whether the style is still being learnt (references uploading, being prepared or measured, or the summary being written). */
export const isLearning = (style: Pick<CreatorStyle, "status"> | undefined): boolean => style?.status === "learning";

function useStyleKey() {
  const { isLoaded, isSignedIn, userId, orgId } = useAuth();
  return { key: [STYLE_QUERY_KEY, userId, orgId] as const, ready: isLoaded && Boolean(isSignedIn) && Boolean(orgId) };
}

/** The creator's style, GET /v1/style. Fetched again every few seconds while it is being learnt, so the progress moves by itself. */
export function useStyle({ enabled = true }: { enabled?: boolean } = {}) {
  const { key, ready } = useStyleKey();
  const getClient = useApiClient();
  return useQuery({
    queryKey: key,
    queryFn: ({ signal }) => fetchStyle(getClient(), signal),
    enabled: enabled && ready,
    retry: retryUnlessClientError,
    refetchInterval: (query) => (isLearning(query.state.data) ? STYLE_POLL_MS : false),
  });
}

/** The answer of a change is the style as it is now: it replaces the cached one. */
function useStyleMutation<A>(fn: (arg: A) => Promise<CreatorStyle | undefined>) {
  const { key } = useStyleKey();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: fn,
    onSuccess: (style) => {
      if (style) queryClient.setQueryData(key, style);
      else void queryClient.invalidateQueries({ queryKey: [STYLE_QUERY_KEY] });
    },
  });
}

/** PUT /v1/style with the creator's settings (the whole set: a setting left out follows the suggestion). */
export function useUpdateStyleSettings() {
  const getClient = useApiClient();
  return useStyleMutation((settings: StyleSettings) => putStyleSettings(getClient(), settings));
}

/** POST /v1/style/relearn. */
export function useRelearnStyle() {
  const getClient = useApiClient();
  return useStyleMutation(() => relearnStyle(getClient()));
}

/** DELETE /v1/style/references/{id}; the style is fetched again. */
export function useRemoveStyleReference() {
  const getClient = useApiClient();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (assetId: string) => removeStyleReference(getClient(), assetId),
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: [STYLE_QUERY_KEY] });
      void queryClient.invalidateQueries({ queryKey: [ASSETS_QUERY_KEY] });
    },
  });
}

/** DELETE /v1/style; the style is fetched again. */
export function useResetStyle() {
  const getClient = useApiClient();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: () => resetStyle(getClient()),
    onSettled: () => void queryClient.invalidateQueries({ queryKey: [STYLE_QUERY_KEY] }),
  });
}
