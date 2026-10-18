import { useAuth } from "@clerk/tanstack-react-start";
import { useQueryClient } from "@tanstack/react-query";
import { createContext, type ReactNode, useContext, useEffect, useMemo, useRef, useState, useSyncExternalStore } from "react";
import { notify } from "@/components/ui/toast";
import { useApiClient } from "@/lib/api/provider";
import { ASSETS_QUERY_KEY, ME_QUERY_KEY, STYLE_QUERY_KEY, isPlanRequired } from "@/lib/query-client";
import { createUploadApi } from "./api";
import type { PickedFile } from "./files";
import { type AddResult, UploadManager, type UploadSnapshot } from "./manager";
import { createUploadStore } from "./store";
import { createXhrTransport } from "./transport";

const EMPTY: UploadSnapshot = {
  items: [],
  summary: { total: 0, active: 0, queued: 0, needsFile: 0, failed: 0, done: 0, bytesTotal: 0, bytesDone: 0, speed: 0, etaSeconds: null },
  ready: false,
};

/** The commands of the upload queue. The object is stable for the life of a manager: it does not change as progress comes in. */
export interface UploadCommands {
  /** False outside a signed-in workspace: there is nothing to upload to. */
  available: boolean;
  /** Adds files to the queue and tells the user about those that were left out. `purpose: "style_reference"` for style videos. */
  addFiles(files: readonly (File | PickedFile)[], options?: { purpose?: "style_reference" }): Promise<AddResult | null>;
  /** `abortOnServer: false` when the caller deletes the asset itself. */
  cancel(id: string, options?: { abortOnServer?: boolean }): void;
  retry(id: string): void;
  retryAllFailed(): void;
  clearDone(): void;
}

/** What the queue looks like, as an external store: read it with {@link useUploadSelector}, or {@link useUploadSnapshot} for all of it. */
export interface UploadStateStore {
  subscribe(listener: () => void): () => void;
  getSnapshot(): UploadSnapshot;
}

const NO_COMMANDS: UploadCommands = { available: false, addFiles: async () => null, cancel: () => {}, retry: () => {}, retryAllFailed: () => {}, clearDone: () => {} };
const EMPTY_STORE: UploadStateStore = { subscribe: () => () => {}, getSnapshot: () => EMPTY };

/**
 * Two contexts, so that something that only adds files or cancels does not render again on every progress tick (and a
 * grid of clips renders when a percent changes, not 6 times a second). Exported for tests.
 */
export const UploadCommandsContext = createContext<UploadCommands | null>(null);
export const UploadStateContext = createContext<UploadStateStore | null>(null);

function describeLeftOut(result: AddResult): { title: string; description: string } | null {
  if (result.rejected.length === 0) return null;
  const first = result.rejected[0];
  return {
    title: result.rejected.length === 1 ? `“${first?.name}” was not added` : `${result.rejected.length} files were not added`,
    description:
      result.rejected.length === 1
        ? (first?.reason ?? "")
        : result.rejected
            .slice(0, 3)
            .map((r) => `${r.name}: ${r.reason}`)
            .join(" "),
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
      refresh = setTimeout(() => {
        void queryClient.invalidateQueries({ queryKey: [ASSETS_QUERY_KEY] });
        void queryClient.invalidateQueries({ queryKey: [STYLE_QUERY_KEY] });
      }, 500);
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

  const state = useMemo<UploadStateStore>(() => (manager ? { subscribe: manager.subscribe, getSnapshot: manager.getSnapshot } : EMPTY_STORE), [manager]);

  // Leaving the page now would interrupt an upload (it could be resumed, but only after the files are picked again).
  const busyRef = useRef(false);
  useEffect(() => {
    if (!manager) return;
    const update = () => {
      const { summary } = manager.getSnapshot();
      busyRef.current = summary.active + summary.queued > 0;
    };
    update();
    return manager.subscribe(update);
  }, [manager]);
  useEffect(() => {
    const onBeforeUnload = (event: BeforeUnloadEvent) => {
      if (!busyRef.current) return;
      event.preventDefault();
      event.returnValue = "";
    };
    window.addEventListener("beforeunload", onBeforeUnload);
    return () => window.removeEventListener("beforeunload", onBeforeUnload);
  }, []);

  const commands = useMemo<UploadCommands>(
    () =>
      manager
        ? {
            available: true,
            async addFiles(files, options) {
              const result = await manager.add(files, options);
              const leftOut = describeLeftOut(result);
              if (leftOut) notify(leftOut.title, { description: leftOut.description, type: "error" });
              return result;
            },
            cancel: (id, options) => manager.cancel(id, options),
            retry: (id) => manager.retry(id),
            retryAllFailed: () => manager.retryAllFailed(),
            clearDone: () => manager.clearDone(),
          }
        : NO_COMMANDS,
    [manager],
  );
  return (
    <UploadCommandsContext.Provider value={commands}>
      <UploadStateContext.Provider value={state}>{children}</UploadStateContext.Provider>
    </UploadCommandsContext.Provider>
  );
}

/** The stable commands (add, cancel, retry...). Does not cause a render when progress changes. */
export function useUploadCommands(): UploadCommands {
  const commands = useContext(UploadCommandsContext);
  if (!commands) throw new Error("useUploadCommands needs an <UploadProvider> above it (the root route has one).");
  return commands;
}

/**
 * A part of the queue's state. The component renders again only when the selected value changes, as `isEqual` sees it
 * (`Object.is` by default), not on every progress tick.
 */
export function useUploadSelector<T>(select: (snapshot: UploadSnapshot) => T, isEqual: (a: T, b: T) => boolean = Object.is): T {
  const store = useContext(UploadStateContext);
  if (!store) throw new Error("useUploadSelector needs an <UploadProvider> above it (the root route has one).");
  const cache = useRef<{ snapshot: UploadSnapshot; value: T } | null>(null);
  const read = () => {
    const snapshot = store.getSnapshot();
    const last = cache.current;
    if (last && last.snapshot === snapshot) return last.value;
    const value = select(snapshot);
    const kept = last && isEqual(last.value, value) ? last.value : value;
    cache.current = { snapshot, value: kept };
    return kept;
  };
  return useSyncExternalStore(store.subscribe, read, read);
}

/** The whole queue; renders on every change. For the tray, which shows every file. */
export function useUploadSnapshot(): UploadSnapshot {
  return useUploadSelector((snapshot) => snapshot);
}
