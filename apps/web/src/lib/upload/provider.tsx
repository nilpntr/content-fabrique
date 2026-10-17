import { useAuth } from "@clerk/tanstack-react-start";
import { useQueryClient } from "@tanstack/react-query";
import { createContext, type ReactNode, useCallback, useContext, useEffect, useMemo, useRef, useState, useSyncExternalStore } from "react";
import { notify } from "@/components/ui/toast";
import { useApiClient } from "@/lib/api/provider";
import { ASSETS_QUERY_KEY, ME_QUERY_KEY, isPlanRequired } from "@/lib/query-client";
import { createUploadApi } from "./api";
import type { PickedFile } from "./files";
import { type AddResult, type UploadSnapshot, UploadManager } from "./manager";
import { createUploadStore } from "./store";
import { createXhrTransport } from "./transport";

const EMPTY: UploadSnapshot = {
  items: [],
  summary: { total: 0, active: 0, queued: 0, needsFile: 0, failed: 0, done: 0, bytesTotal: 0, bytesDone: 0, speed: 0, etaSeconds: null },
  ready: false,
};

export interface UploadsApi {
  snapshot: UploadSnapshot;
  /** False outside a signed-in workspace: there is nothing to upload to. */
  available: boolean;
  /** Adds files to the queue and tells the user about those that were left out. */
  addFiles(files: readonly (File | PickedFile)[]): Promise<AddResult | null>;
  cancel(id: string): void;
  retry(id: string): void;
  retryAllFailed(): void;
  clearDone(): void;
}

/** Exported for tests, which render the tray and the buttons over a hand-made {@link UploadsApi}. */
export const UploadsContext = createContext<UploadsApi | null>(null);

function describeLeftOut(result: AddResult): { title: string; description: string } | null {
  if (result.rejected.length === 0) return null;
  const first = result.rejected[0];
  return {
    title: result.rejected.length === 1 ? `“${first?.name}” was not added` : `${result.rejected.length} files were not added`,
    description: result.rejected.length === 1 ? (first?.reason ?? "") : result.rejected.slice(0, 3).map((r) => `${r.name}: ${r.reason}`).join(" "),
  };
}

/**
 * The one upload manager of the signed-in workspace, for the whole app (it lives in the root, so uploads go on while you
 * move between pages). A workspace switch stops the old manager (what it has not finished stays stored, for when you come
 * back) and starts one for the new workspace. Closing the tab with uploads running asks first.
 */
export function UploadProvider({ children }: { children: ReactNode }) {
  const { isLoaded, isSignedIn, userId, orgId } = useAuth();
  const getClient = useApiClient();
  const queryClient = useQueryClient();
  const workspace = isLoaded && isSignedIn && userId && orgId ? `${userId}:${orgId}` : null;
  const [manager, setManager] = useState<UploadManager | null>(null);

  useEffect(() => {
    if (!workspace || typeof window === "undefined") return;
    let refresh: ReturnType<typeof setTimeout> | undefined;
    const created = new UploadManager({
      api: createUploadApi(getClient),
      transport: createXhrTransport(),
      store: createUploadStore(),
      workspace,
      onApiError: (error) => {
        if (isPlanRequired(error)) void queryClient.invalidateQueries({ queryKey: [ME_QUERY_KEY] });
      },
    });
    // The library lists what the server has: tell it when an upload starts, finishes or is removed (at most twice a second).
    const stopEvents = created.subscribeEvents(() => {
      clearTimeout(refresh);
      refresh = setTimeout(() => void queryClient.invalidateQueries({ queryKey: [ASSETS_QUERY_KEY] }), 500);
    });
    void created.init();
    setManager(created);
    return () => {
      clearTimeout(refresh);
      stopEvents();
      created.dispose();
      setManager(null);
    };
  }, [workspace, getClient, queryClient]);

  const subscribe = useCallback((listener: () => void) => (manager ? manager.subscribe(listener) : () => {}), [manager]);
  const getSnapshot = useCallback(() => manager?.getSnapshot() ?? EMPTY, [manager]);
  const snapshot = useSyncExternalStore(subscribe, getSnapshot, () => EMPTY);

  // Leaving the page now would interrupt an upload (it could be resumed, but only after the files are picked again).
  const busy = snapshot.summary.active + snapshot.summary.queued > 0;
  const busyRef = useRef(busy);
  busyRef.current = busy;
  useEffect(() => {
    const onBeforeUnload = (event: BeforeUnloadEvent) => {
      if (!busyRef.current) return;
      event.preventDefault();
      event.returnValue = "";
    };
    window.addEventListener("beforeunload", onBeforeUnload);
    return () => window.removeEventListener("beforeunload", onBeforeUnload);
  }, []);

  const api = useMemo<UploadsApi>(
    () => ({
      snapshot,
      available: manager !== null,
      async addFiles(files) {
        if (!manager) return null;
        const result = await manager.add(files);
        const leftOut = describeLeftOut(result);
        if (leftOut) notify(leftOut.title, { description: leftOut.description, type: "error" });
        return result;
      },
      cancel: (id) => manager?.cancel(id),
      retry: (id) => manager?.retry(id),
      retryAllFailed: () => manager?.retryAllFailed(),
      clearDone: () => manager?.clearDone(),
    }),
    [manager, snapshot],
  );
  return <UploadsContext.Provider value={api}>{children}</UploadsContext.Provider>;
}

export function useUploads(): UploadsApi {
  const api = useContext(UploadsContext);
  if (!api) throw new Error("useUploads needs an <UploadProvider> above it (the root route has one).");
  return api;
}
