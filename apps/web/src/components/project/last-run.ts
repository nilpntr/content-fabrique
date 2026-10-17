import { useCallback, useSyncExternalStore } from "react";

/**
 * Which editor run a project's page shows.
 *
 * TODO(P4-06): the API has no "runs of a project" call, so this is a stand-in. When it has one (or `Project` names its
 * active run), the page asks the server and this module, `rememberRun` and the `?run=` parameter can go.
 *
 * Until then the order is: the run in the URL (`?run=`); else the run that made the newest version, if that version is newer
 * than the run this browser remembers; else the remembered run (the one this browser started last).
 */

/** A run id as the API issues them. The one validator: the URL, storage and version authors all go through it. */
export const isRunId = (value: unknown): value is string => typeof value === "string" && /^run_[0-9a-z]{26}$/.test(value);

export interface RememberedRun {
  id: string;
  /** When the run was created (ISO 8601), to tell whether a newer version came after it. */
  createdAt: string;
}

const KEY = (projectId: string) => `cf:last-run:${projectId}`;
const CHANGED = "cf:last-run-changed";

export function rememberRun(projectId: string, runId: string, createdAt: string = new Date().toISOString()): void {
  try {
    window.localStorage.setItem(KEY(projectId), JSON.stringify({ id: runId, createdAt } satisfies RememberedRun));
    window.dispatchEvent(new Event(CHANGED));
  } catch {
    // Storage is blocked or full: the URL still carries the run.
  }
}

/** The raw stored text, so that `useSyncExternalStore` can compare snapshots by value. */
function readRaw(projectId: string): string | null {
  try {
    return window.localStorage.getItem(KEY(projectId));
  } catch {
    return null;
  }
}

/** The remembered run, or undefined when there is none or what is stored is not a run. */
export function parseRemembered(raw: string | null): RememberedRun | undefined {
  if (!raw) return undefined;
  try {
    const value = JSON.parse(raw) as Partial<RememberedRun> | null;
    if (value && isRunId(value.id) && typeof value.createdAt === "string") return { id: value.id, createdAt: value.createdAt };
  } catch {
    // An old or damaged value is no run.
  }
  return undefined;
}

export function recallRun(projectId: string): RememberedRun | undefined {
  return parseRemembered(readRaw(projectId));
}

/**
 * The remembered run, read from storage in the browser only: the server and the first client render see nothing, so the
 * HTML matches, and the real value follows after hydration (or when another tab changes it).
 */
export function useRememberedRun(projectId: string): RememberedRun | undefined {
  const subscribe = useCallback((onChange: () => void) => {
    window.addEventListener("storage", onChange);
    window.addEventListener(CHANGED, onChange);
    return () => {
      window.removeEventListener("storage", onChange);
      window.removeEventListener(CHANGED, onChange);
    };
  }, []);
  const raw = useSyncExternalStore(
    subscribe,
    () => readRaw(projectId),
    () => null,
  );
  return parseRemembered(raw);
}

/** The run to show: see the order above. `newest` is the newest version made by a run. */
export function chooseRun(urlRun: string | undefined, remembered: RememberedRun | undefined, newest: { runId: string; createdAt: string } | undefined): string | undefined {
  if (isRunId(urlRun)) return urlRun;
  const newestRun = newest && isRunId(newest.runId) ? newest : undefined;
  if (newestRun && (!remembered || Date.parse(newestRun.createdAt) > Date.parse(remembered.createdAt))) return newestRun.runId;
  return remembered?.id;
}
