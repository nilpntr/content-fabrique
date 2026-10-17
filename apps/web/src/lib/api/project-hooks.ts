import { useAuth } from "@clerk/tanstack-react-start";
import { type QueryClient, useInfiniteQuery, useMutation, useQueries, useQuery, useQueryClient } from "@tanstack/react-query";
import { useCallback, useEffect, useMemo, useRef } from "react";
import {
  type ApiClient,
  ApiError,
  cancelRun,
  createProject,
  createRender,
  fetchProject,
  fetchProjects,
  fetchRender,
  fetchRenders,
  fetchRun,
  fetchTimelineVersion,
  fetchTimelineVersions,
  type Render,
  RUN_EVENTS_PAGE,
  type Run,
  type RunEvent,
  type RunStatus,
  replyToRun,
  type StartEditRequest,
  startEdit,
  type TimelineVersion,
  type TimelineVersionSummary,
} from "@/lib/api/client";
import { retryUnlessClientError } from "@/lib/api/hooks";
import { useApiClient } from "@/lib/api/provider";
import { parseTimeMap } from "@/components/project/review-data";
import type { TimeMapEntry } from "@/components/editor/types";
import { URL_REFRESH_MIN_MS, useRefreshThrottle } from "@/lib/refresh-throttle";
import { ANNOTATIONS_QUERY_KEY, PROJECT_QUERY_KEY, PROJECTS_QUERY_KEY, RENDER_QUERY_KEY, RENDERS_QUERY_KEY, RUN_QUERY_KEY, TIMELINES_QUERY_KEY } from "@/lib/query-client";

/**
 * The hooks of projects, runs and renders. Keys are `[prefix, userId, orgId, ...ids]`, like the library's, so a workspace
 * switch fetches again; the prefixes are in query-client.ts. Everything that changes a project (an edit run starting or
 * ending, a new project) invalidates `PROJECT_QUERY_KEY`, `TIMELINES_QUERY_KEY` and `PROJECTS_QUERY_KEY` for it.
 */

function useScope() {
  const { isLoaded, isSignedIn, userId, orgId } = useAuth();
  return { userId, orgId, ready: isLoaded && Boolean(isSignedIn) && Boolean(orgId) };
}

/** The projects of the workspace, newest first; pages on demand. */
export function useProjects() {
  const { userId, orgId, ready } = useScope();
  const getClient = useApiClient();
  return useInfiniteQuery({
    queryKey: [PROJECTS_QUERY_KEY, userId, orgId],
    queryFn: ({ pageParam, signal }) => fetchProjects(getClient(), pageParam, signal),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (page) => (typeof page.next_cursor === "string" ? page.next_cursor : undefined),
    select: (data) => data.pages.flatMap((page) => (Array.isArray(page.items) ? page.items : [])),
    enabled: ready,
    retry: retryUnlessClientError,
  });
}

export function useProject(projectId: string) {
  const { userId, orgId, ready } = useScope();
  const getClient = useApiClient();
  return useQuery({
    queryKey: [PROJECT_QUERY_KEY, userId, orgId, projectId],
    queryFn: ({ signal }) => fetchProject(getClient(), projectId, signal),
    enabled: ready,
    retry: retryUnlessClientError,
  });
}

/** The versions of the project's timeline, newest first, without their specs. */
export function useTimelineVersions(projectId: string) {
  const { userId, orgId, ready } = useScope();
  const getClient = useApiClient();
  return useQuery({
    queryKey: [TIMELINES_QUERY_KEY, userId, orgId, projectId],
    queryFn: ({ signal }) => fetchTimelineVersions(getClient(), projectId, signal),
    enabled: ready,
    retry: retryUnlessClientError,
  });
}

/**
 * The specs of the given versions. A version never changes once made, so each is fetched once and kept (the review needs
 * them all: a comment is moved between versions through their time maps).
 */
export function useTimelineSpecs(projectId: string, versions: readonly TimelineVersionSummary[]) {
  const { userId, orgId, ready } = useScope();
  const getClient = useApiClient();
  return useQueries({
    queries: versions.map((summary) => ({
      queryKey: [TIMELINES_QUERY_KEY, userId, orgId, projectId, summary.version],
      queryFn: ({ signal }: { signal: AbortSignal }): Promise<TimelineVersion> => fetchTimelineVersion(getClient(), projectId, summary.version, signal),
      enabled: ready,
      staleTime: Number.POSITIVE_INFINITY,
      retry: retryUnlessClientError,
    })),
  });
}

/** Everything a project's page shows is fetched again: after an edit run starts or ends, or when the project changes. */
export function invalidateProject(queryClient: QueryClient) {
  void queryClient.invalidateQueries({ queryKey: [PROJECT_QUERY_KEY] });
  // Only the lists of versions (`[TIMELINES_QUERY_KEY, user, org, projectId]`): a version's own spec, one key longer, never changes.
  void queryClient.invalidateQueries({ queryKey: [TIMELINES_QUERY_KEY], predicate: (query) => query.queryKey.length === 4 });
  void queryClient.invalidateQueries({ queryKey: [PROJECTS_QUERY_KEY] });
  // A finished run marks the comments it dealt with as applied, and answers under them.
  void queryClient.invalidateQueries({ queryKey: [ANNOTATIONS_QUERY_KEY] });
}

export function useCreateProject() {
  const getClient = useApiClient();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (body: Parameters<typeof createProject>[1]) => createProject(getClient(), body),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: [PROJECTS_QUERY_KEY] }),
  });
}

// ── Runs ────────────────────────────────────────────────────────────────────

const ACTIVE_RUN_STATUSES: ReadonlySet<RunStatus> = new Set(["queued", "running", "waiting_input"]);

/** A run that is not finished: queued, running or waiting for the creator's answer. */
export const isRunActive = (status: RunStatus | undefined): boolean => status !== undefined && ACTIVE_RUN_STATUSES.has(status);

/** How often a run is asked about while it is active: 1.5 s, and 2.5 s while it only waits for the creator. */
export function runPollDelay(status: RunStatus | undefined): number | false {
  if (status === "queued" || status === "running") return 1500;
  if (status === "waiting_input") return 2500;
  return false;
}

/** The events of both, by `seq`, oldest first. */
export function mergeEvents(known: readonly RunEvent[], more: readonly RunEvent[]): RunEvent[] {
  const bySeq = new Map<number, RunEvent>();
  for (const event of [...known, ...more]) bySeq.set(event.seq, event);
  return [...bySeq.values()].sort((a, b) => a.seq - b.seq);
}

const lastSeq = (events: readonly RunEvent[]) => events.reduce((max, event) => Math.max(max, event.seq), 0);

/** The run as the API answered, with the events it had before kept: GET /v1/runs/{id} only returns the ones after a sequence number. */
function withKnownEvents(next: Run, previous: Run | undefined): Run {
  return previous ? { ...next, events: mergeEvents(previous.events ?? [], next.events ?? []) } : next;
}

async function fetchRunFeed(client: ApiClient, runId: string, previous: Run | undefined, signal: AbortSignal): Promise<Run> {
  let events = previous?.events ?? [];
  let run: Run | undefined;
  // Only the events after the ones we have; a long run is read in pages.
  for (let page = 0; page < 10; page++) {
    run = await fetchRun(client, runId, lastSeq(events), signal);
    const got = Array.isArray(run.events) ? run.events : [];
    events = mergeEvents(events, got);
    if (got.length < RUN_EVENTS_PAGE) break;
  }
  return { ...(run as Run), events };
}

/**
 * A run, fetched again every 1.5 s while it is active (see {@link runPollDelay}) and never once it has finished. Events
 * accumulate across polls. Every component that shows the run can use this; they share one query.
 */
export function useRunQuery(runId: string | undefined) {
  const { userId, orgId, ready } = useScope();
  const getClient = useApiClient();
  const queryClient = useQueryClient();
  const key = [RUN_QUERY_KEY, userId, orgId, runId];
  return useQuery({
    queryKey: key,
    queryFn: ({ signal }) => fetchRunFeed(getClient(), runId as string, queryClient.getQueryData<Run>(key), signal),
    enabled: ready && Boolean(runId),
    // Two components on a page show the same run: the second one mounting must not ask again at once.
    staleTime: 1000,
    retry: retryUnlessClientError,
    refetchInterval: (q) => runPollDelay(q.state.data?.status),
  });
}

/**
 * {@link useRunQuery}, and when the run leaves an active state the project, its versions and the list are fetched again,
 * because the run made versions. Use it once per page.
 */
export function useRun(runId: string | undefined) {
  const queryClient = useQueryClient();
  const query = useRunQuery(runId);
  const status = query.data?.status;
  const previous = useRef<RunStatus | undefined>(undefined);
  useEffect(() => {
    if (isRunActive(previous.current) && status !== undefined && !isRunActive(status)) invalidateProject(queryClient);
    previous.current = status;
  }, [status, queryClient]);
  return query;
}

/** Starts an editor run on a project; the answer is the queued run, which is put in the cache so that its panel has it at once. */
export function useStartEdit() {
  const { userId, orgId } = useScope();
  const getClient = useApiClient();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ projectId, body }: { projectId: string; body: StartEditRequest }) => startEdit(getClient(), projectId, body),
    onSuccess: (run) => {
      queryClient.setQueryData([RUN_QUERY_KEY, userId, orgId, run.id], run);
      invalidateProject(queryClient);
    },
  });
}

function useRunCommand(mutate: (client: ApiClient, runId: string, arg: string) => Promise<Run>) {
  const { userId, orgId } = useScope();
  const getClient = useApiClient();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ runId, text }: { runId: string; text: string }) => mutate(getClient(), runId, text),
    onSuccess: (run) => {
      const key = [RUN_QUERY_KEY, userId, orgId, run.id];
      queryClient.setQueryData<Run>(key, (previous) => withKnownEvents(run, previous));
      void queryClient.invalidateQueries({ queryKey: key });
    },
  });
}

export function useCancelRun() {
  return useRunCommand((client, runId) => cancelRun(client, runId));
}

export function useReplyToRun() {
  return useRunCommand((client, runId, text) => replyToRun(client, runId, text));
}

// ── Renders ─────────────────────────────────────────────────────────────────

/** What the review screen knows about the preview of a version. */
export type PreviewRenderState =
  | { kind: "loading" }
  /** The API cannot render yet (P3-06 is not deployed): there is no preview to wait for. */
  | { kind: "unavailable" }
  /** The renders could not be listed. */
  | { kind: "error"; message: string }
  /** No preview of this version exists. */
  | { kind: "none" }
  /** Queued or rendering. */
  | { kind: "working"; render: Render }
  | { kind: "ready"; render: Render; mp4Url: string }
  | { kind: "failed"; render: Render };

const URL_REFRESH_MARGIN_MS = 60_000;

/** Which of a version's renders to show: a ready one, else one in progress, else the latest failure. The list is newest first. */
export function pickRender(renders: readonly Render[]): Render | undefined {
  return renders.find((render) => render.status === "ready") ?? renders.find((render) => render.status === "queued" || render.status === "rendering") ?? renders[0];
}

/** An answer that means the API has no such endpoint. */
const isMissingEndpoint = (error: unknown) => error instanceof ApiError && (error.status === 404 || error.status === 405 || error.status === 501);

/**
 * The preview render of a version: found through the project's render list, then followed through GET /v1/renders/{id}
 * (polled every 2 s while it works; its presigned URLs are renewed shortly before they expire). `request` queues one when
 * there is none (or the last failed). `refreshUrls` fetches the render again for fresh URLs, at most every 10 s: call it
 * when the video fails to load.
 */
export function usePreviewRender(projectId: string, version: number | undefined) {
  const { userId, orgId, ready } = useScope();
  const getClient = useApiClient();
  const queryClient = useQueryClient();
  const listKey = [RENDERS_QUERY_KEY, userId, orgId, projectId, version];
  const list = useQuery({
    queryKey: listKey,
    queryFn: ({ signal }) => fetchRenders(getClient(), projectId, version as number, "preview", signal),
    enabled: ready && version !== undefined,
    retry: (count, error) => !isMissingEndpoint(error) && retryUnlessClientError(count, error),
  });
  const chosen = useMemo(() => pickRender(list.data ?? []), [list.data]);
  const detailKey = [RENDER_QUERY_KEY, userId, orgId, chosen?.id];
  const detail = useQuery({
    queryKey: detailKey,
    queryFn: ({ signal }) => fetchRender(getClient(), chosen?.id as string, signal),
    enabled: ready && Boolean(chosen),
    staleTime: 0,
    retry: retryUnlessClientError,
    refetchInterval: (q) => {
      const render = q.state.data;
      if (render?.status === "queued" || render?.status === "rendering") return 2000;
      const expires = render?.status === "ready" && render.urls_expires_at ? Date.parse(render.urls_expires_at) : Number.NaN;
      return Number.isFinite(expires) ? Math.max(URL_REFRESH_MIN_MS, Math.min(2 ** 31 - 1, expires - Date.now() - URL_REFRESH_MARGIN_MS)) : false;
    },
  });

  const chosenId = chosen?.id;
  const { refresh: refreshUrls } = useRefreshThrottle(() => void queryClient.invalidateQueries({ queryKey: [RENDER_QUERY_KEY, userId, orgId, chosenId] }));

  // The variable is the version asked for, so that the state below is the one of the version on screen, not of another that was asked for a moment ago.
  const request = useMutation({
    mutationFn: (requested: number) => createRender(getClient(), projectId, requested, "preview"),
    onSuccess: (render) => {
      // The render says which version it is of; that may no longer be the one on screen.
      const key = [RENDERS_QUERY_KEY, userId, orgId, projectId, render.timeline_version];
      queryClient.setQueryData([RENDER_QUERY_KEY, userId, orgId, render.id], render);
      // The list names it at once; the fetch that follows confirms.
      queryClient.setQueryData<Render[]>(key, (old) => [render, ...(old ?? []).filter((candidate) => candidate.id !== render.id)]);
      void queryClient.invalidateQueries({ queryKey: key });
    },
  });
  const asked = request.variables === version;
  const requesting = request.isPending && asked;
  const requestError = request.isError && asked ? request.error : undefined;
  const requestRender = useCallback(() => {
    if (version !== undefined) request.mutate(version);
  }, [request, version]);

  // A render that finished: the list is fetched again so that it names the same render next time.
  const detailStatus = detail.data?.status;
  const wasWorking = useRef(false);
  useEffect(() => {
    if (wasWorking.current && (detailStatus === "ready" || detailStatus === "failed")) {
      void queryClient.invalidateQueries({ queryKey: [RENDERS_QUERY_KEY, userId, orgId, projectId, version] });
    }
    wasWorking.current = detailStatus === "queued" || detailStatus === "rendering";
  }, [detailStatus, queryClient, userId, orgId, projectId, version]);

  let state: PreviewRenderState;
  if (version === undefined) state = { kind: "none" };
  else if (list.isError) {
    state = isMissingEndpoint(list.error) ? { kind: "unavailable" } : { kind: "error", message: list.error instanceof ApiError ? list.error.message : "The previews could not be loaded." };
  } else if (list.isPending) state = { kind: "loading" };
  else if (!chosen) state = { kind: "none" };
  else {
    const render = detail.data ?? chosen;
    if (render.status === "failed") state = { kind: "failed", render };
    else if (render.status === "ready") state = render.files?.mp4.url ? { kind: "ready", render, mp4Url: render.files.mp4.url } : { kind: "loading" };
    else state = { kind: "working", render };
  }

  return { state, requestRender, requesting, requestError, refreshUrls, timeMapUrl: state.kind === "ready" ? state.render.files?.time_map.url : undefined };
}

/**
 * The render's time map: a JSON file behind a presigned URL. Undefined while it loads and when it cannot be read (the caller
 * then uses the time map made from the spec, which is the same by construction). The URL is part of the key, so a failure is
 * tried again with the fresh URL that a refresh brings, and is not remembered.
 */
export function useRenderTimeMap(renderId: string | undefined, url: string | undefined): TimeMapEntry[] | undefined {
  const { userId, orgId, ready } = useScope();
  const query = useQuery({
    queryKey: [RENDER_QUERY_KEY, userId, orgId, renderId, "time-map", url],
    queryFn: async (): Promise<TimeMapEntry[]> => {
      const response = await fetch(url as string, { credentials: "omit" });
      if (!response.ok) throw new Error(`The time map answered ${response.status}.`);
      const entries = parseTimeMap(await response.json());
      if (!entries) throw new Error("The time map is not a list.");
      return entries;
    },
    enabled: ready && Boolean(renderId && url),
    staleTime: Number.POSITIVE_INFINITY,
    retry: 1,
  });
  return query.data;
}

