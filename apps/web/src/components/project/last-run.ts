/**
 * The editor run a project's page shows. The API has no "runs of a project" call, so the run that was started from this
 * browser is remembered here (and carried in the URL as `?run=`); a page opened without either falls back to the run that
 * made the latest version (`TimelineVersionSummary.author_id`).
 */
const KEY = (projectId: string) => `cf:last-run:${projectId}`;

export function rememberRun(projectId: string, runId: string): void {
  try {
    window.localStorage.setItem(KEY(projectId), runId);
  } catch {
    // Storage is blocked or full: the URL still carries the run.
  }
}

export function recallRun(projectId: string): string | undefined {
  try {
    return window.localStorage.getItem(KEY(projectId)) ?? undefined;
  } catch {
    return undefined;
  }
}

/** A run id as the API issues them. Anything else in the URL is ignored. */
export const isRunId = (value: unknown): value is string => typeof value === "string" && /^run_[0-9a-z]{26}$/.test(value);
