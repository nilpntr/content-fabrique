import type { ReviewAnnotation } from "@/components/editor/types";

/**
 * Where a project's review comments live. The annotations API (P1-08) does not exist yet, so the only implementation keeps
 * them in memory: they survive moving around the app and are gone on a reload. When the API lands, a store that calls it
 * replaces `sessionCommentsStore` and nothing else changes; the screen says "comments aren't saved yet" until then.
 */
export interface CommentsStore {
  list(projectId: string): ReviewAnnotation[];
  /** Replaces the project's comments with these: every change on the screen (add, edit, resolve, delete) is written this way. */
  replace(projectId: string, comments: readonly ReviewAnnotation[]): void;
}

export function createMemoryCommentsStore(): CommentsStore {
  const byProject = new Map<string, ReviewAnnotation[]>();
  return {
    list: (projectId) => [...(byProject.get(projectId) ?? [])],
    replace: (projectId, comments) => {
      byProject.set(projectId, [...comments]);
    },
  };
}

/** The store of this browser tab. */
export const sessionCommentsStore: CommentsStore = createMemoryCommentsStore();

/** Shown wherever comments can be written. */
export const COMMENTS_NOT_SAVED_NOTE = "Comments aren't saved yet. They stay until you reload the page.";
