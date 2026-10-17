import { useAuth } from "@clerk/tanstack-react-start";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useCallback, useMemo, useState } from "react";
import { commentMessage } from "@/components/project/errors";
import { type Annotation, type AnnotationTargetType, createAnnotation, deleteAnnotation, fetchAnnotations, updateAnnotation } from "@/lib/api/client";
import { retryUnlessClientError, useMe } from "@/lib/api/hooks";
import { useApiClient } from "@/lib/api/provider";
import { ANNOTATIONS_QUERY_KEY } from "@/lib/query-client";

/**
 * The comments of a project (P1-08): every comment and reply on its renders and versions, oldest first, from
 * GET /v1/annotations?project_id=. The key is `[ANNOTATIONS_QUERY_KEY, user, org, projectId]` like the other project queries.
 */
export function useAnnotations(projectId: string) {
  const { isLoaded, isSignedIn, userId, orgId } = useAuth();
  const getClient = useApiClient();
  return useQuery({
    queryKey: [ANNOTATIONS_QUERY_KEY, userId, orgId, projectId],
    queryFn: ({ signal }) => fetchAnnotations(getClient(), projectId, signal),
    enabled: isLoaded && Boolean(isSignedIn) && Boolean(orgId),
    retry: retryUnlessClientError,
  });
}

/** What a new comment is about: the ready render on screen, or the version when it has none. */
export interface CommentTarget {
  type: Extract<AnnotationTargetType, "render" | "timeline_version">;
  id: string;
}

/** A comment as the review screen made it, before the API has seen it. */
export interface NewComment {
  /** The screen's own id (`ann_local_...`): the comment shows under it until the API answers. */
  tempId: string;
  version: number;
  start_ms: number;
  end_ms: number;
  pin?: { x: number; y: number };
  body: string;
}

/** What a person can change in a comment. */
export interface CommentPatch {
  body?: string;
  status?: "open" | "resolved";
}

/** Put the saved comment where the placeholder was, or add it when a refetch has taken the placeholder away meanwhile. */
const settle = (list: Annotation[], id: string, saved: Annotation) =>
  list.some((a) => a.id === id) ? list.map((a) => (a.id === id ? saved : a)) : [...list.filter((a) => a.id !== saved.id), saved].sort(byId);

const byId = (a: Annotation, b: Annotation) => (a.id < b.id ? -1 : a.id > b.id ? 1 : 0);

/**
 * The writes of the review screen's comments. Each one changes the cached list at once (so the screen never waits for the
 * network) and then asks the API; when the API refuses, only that change is taken back and `error` says why in words for the
 * creator. A saved comment replaces its placeholder with the one the API returned.
 */
export function useCommentActions(projectId: string) {
  const { userId, orgId } = useAuth();
  const getClient = useApiClient();
  const queryClient = useQueryClient();
  const me = useMe().data?.user.id;
  const key = useMemo(() => [ANNOTATIONS_QUERY_KEY, userId, orgId, projectId], [userId, orgId, projectId]);
  const [error, setError] = useState<string | undefined>();

  // A refetch that is on its way was asked for before this change and would answer without it: it is cancelled first (synchronously,
  // so the screen sees the change at once), and the cache is only ever changed by id.
  const edit = useCallback(
    (change: (list: Annotation[]) => Annotation[]) => {
      void queryClient.cancelQueries({ queryKey: key });
      return queryClient.setQueryData<Annotation[]>(key, (old) => change(old ?? []));
    },
    [queryClient, key],
  );
  const current = useCallback(() => queryClient.getQueryData<Annotation[]>(key) ?? [], [queryClient, key]);
  const fail = useCallback((e: unknown) => setError(commentMessage(e)), []);

  const create = useCallback(
    async (input: NewComment, target: CommentTarget | undefined): Promise<Annotation | undefined> => {
      setError(undefined);
      const now = new Date().toISOString();
      const placeholder: Annotation = {
        id: input.tempId,
        target_type: target?.type ?? "timeline_version",
        target_id: target?.id ?? "",
        project_id: projectId,
        timeline_version: input.version,
        start_ms: input.start_ms,
        end_ms: input.end_ms,
        pin: input.pin ? { x: input.pin.x, y: input.pin.y } : null,
        kind: "comment",
        body: input.body,
        status: "open",
        parent_id: null,
        // The real id once /v1/me has answered; until then the mapping shows a placeholder (a local id) as the signed-in user's own.
        author: { kind: "user", id: me ?? "usr_pending" },
        applied_in_run_id: null,
        addressed_in_version: null,
        created_at: now,
        updated_at: now,
      };
      edit((list) => [...list, placeholder]);
      try {
        if (!target) throw new Error("This version is not known to the server.");
        const saved = await createAnnotation(getClient(), {
          target_type: target.type,
          target_id: target.id,
          project_id: projectId,
          start_ms: input.start_ms,
          end_ms: input.end_ms,
          body: input.body,
          ...(input.pin ? { pin: { x: input.pin.x, y: input.pin.y } } : {}),
        });
        edit((list) => settle(list, input.tempId, saved));
        return saved;
      } catch (e) {
        edit((list) => list.filter((a) => a.id !== input.tempId));
        fail(e);
        return undefined;
      }
    },
    [edit, fail, getClient, me, projectId],
  );

  const update = useCallback(
    async (id: string, patch: CommentPatch): Promise<Annotation | undefined> => {
      setError(undefined);
      const before = current().find((a) => a.id === id);
      if (!before) return undefined;
      // What the editor applied is not what is written now: the server reopens it, and so does the screen.
      const reopens = before.status === "applied" && patch.body !== undefined && patch.status === undefined;
      const guess: Annotation = { ...before, ...(patch.body !== undefined ? { body: patch.body } : {}), status: patch.status ?? (reopens ? "open" : before.status) };
      if (patch.status !== undefined || reopens) {
        guess.applied_in_run_id = null;
        guess.addressed_in_version = null;
      }
      edit((list) => list.map((a) => (a.id === id ? guess : a)));
      try {
        const saved = await updateAnnotation(getClient(), id, patch);
        edit((list) => settle(list, id, saved));
        return saved;
      } catch (e) {
        edit((list) => list.map((a) => (a.id === id ? before : a)));
        fail(e);
        return undefined;
      }
    },
    [current, edit, fail, getClient],
  );

  const remove = useCallback(
    async (id: string): Promise<boolean> => {
      setError(undefined);
      // The replies go with their comment.
      const gone = current().filter((a) => a.id === id || a.parent_id === id);
      if (gone.length === 0) return false;
      edit((list) => list.filter((a) => a.id !== id && a.parent_id !== id));
      try {
        await deleteAnnotation(getClient(), id);
        // A refetch that started before the delete finished may have brought it back: it is gone for good now.
        edit((list) => list.filter((a) => a.id !== id && a.parent_id !== id));
        return true;
      } catch (e) {
        edit((list) => [...list, ...gone.filter((g) => !list.some((a) => a.id === g.id))].sort(byId));
        fail(e);
        return false;
      }
    },
    [current, edit, fail, getClient],
  );

  return { create, update, remove, error, clearError: useCallback(() => setError(undefined), []) };
}
