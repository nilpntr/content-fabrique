import { useAuth } from "@clerk/tanstack-react-start";
import { createContext, type ReactNode, useCallback, useContext, useRef } from "react";
import { type ApiClient, createApiClient } from "@/lib/api/client";

/** Returns the one client of this signed-in session. */
type GetApiClient = () => ApiClient;

const ApiClientContext = createContext<GetApiClient | null>(null);

/**
 * Provides the API client to the hooks of lib/api/hooks.ts: one client for the whole app, not one per component. It goes
 * inside ClerkProvider (the root route). The client is made on first use, inside a query, so a missing VITE_API_BASE_URL is
 * that query's error and not a crash of the render, and it asks Clerk for the token at each request through a ref, so it
 * never goes stale however often Clerk's `getToken` changes identity. The provider lives and dies with the React tree, so
 * nothing is shared between server requests.
 */
export function ApiClientProvider({ children }: { children: ReactNode }) {
  const { getToken } = useAuth();
  const tokenSource = useRef(getToken);
  tokenSource.current = getToken;
  const client = useRef<ApiClient | undefined>(undefined);
  const getClient = useCallback<GetApiClient>(() => {
    client.current ??= createApiClient({ getToken: () => tokenSource.current() });
    return client.current;
  }, []);
  return <ApiClientContext.Provider value={getClient}>{children}</ApiClientContext.Provider>;
}

/** A function that returns the API client. Call it inside a query function. */
export function useApiClient(): GetApiClient {
  const getClient = useContext(ApiClientContext);
  if (!getClient) throw new Error("useApiClient needs an <ApiClientProvider> above it (the root route has one).");
  return getClient;
}
