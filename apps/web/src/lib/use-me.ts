import { useAuth } from "@clerk/tanstack-react-start";
import { useQuery } from "@tanstack/react-query";
import { ApiError, fetchMe } from "@/lib/api";
import { ME_QUERY_KEY } from "@/lib/query-client";

/**
 * The signed-in user and active workspace, from GET /v1/me. The key holds the Clerk user and organization, so switching
 * workspace fetches again and never shows the previous workspace's data. It runs in the browser only (the token comes
 * from Clerk's browser session), so server rendering shows the loading state.
 */
export function useMe() {
  const { isLoaded, isSignedIn, userId, orgId, getToken } = useAuth();
  return useQuery({
    queryKey: [ME_QUERY_KEY, userId, orgId],
    queryFn: ({ signal }) => fetchMe({ getToken: () => getToken(), signal }),
    enabled: isLoaded && Boolean(isSignedIn) && Boolean(orgId),
    // A 4xx is an answer (no workspace, deleted account), so asking again will not change it; a 5xx or a lost
    // connection is worth a couple more tries.
    retry: (failureCount, error) => failureCount < 2 && !(error instanceof ApiError && error.status >= 400 && error.status < 500),
  });
}
