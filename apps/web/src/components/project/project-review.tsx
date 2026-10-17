import { useQuery } from "@tanstack/react-query";
import { Film, Loader2 } from "lucide-react";
import * as React from "react";
import { ReviewScreen } from "@/components/editor/review-screen";
import type { OutputFormat, ReviewAnnotation, TimeMapEntry } from "@/components/editor/types";
import { Skeleton } from "@/components/ui/skeleton";
import type { Project, TimelineVersionSummary } from "@/lib/api/client";
import { usePreviewRender, useTimelineSpecs } from "@/lib/api/project-hooks";
import { RENDER_QUERY_KEY } from "@/lib/query-client";
import { type CommentsStore, COMMENTS_NOT_SAVED_NOTE } from "./comments-store";
import { renderMessage } from "./errors";
import { PreviewStatus } from "./preview-status";
import { parseTimeMap, toReviewVersion } from "./review-data";
import { VersionHistory } from "./version-history";

/** The render's time map is a JSON file behind a presigned URL. If it cannot be read, the one derived from the spec (the same by construction) is used. */
function useRenderTimeMap(renderId: string | undefined, url: string | undefined): TimeMapEntry[] | undefined {
  const query = useQuery({
    queryKey: [RENDER_QUERY_KEY, "time-map", renderId],
    queryFn: async (): Promise<TimeMapEntry[] | null> => {
      try {
        const response = await fetch(url as string, { credentials: "omit" });
        return response.ok ? (parseTimeMap(await response.json()) ?? null) : null;
      } catch {
        return null;
      }
    },
    enabled: Boolean(renderId && url),
    staleTime: Number.POSITIVE_INFINITY,
    retry: false,
  });
  return query.data ?? undefined;
}

/**
 * The review screen on a project's real data: the versions of its timeline (each version's spec gives the cut marks and the
 * labels; the asset titles name the clips), the preview render of the version on screen as the video, and comments that live
 * in `comments` (in memory for now).
 */
export function ProjectReview({
  project,
  versions,
  comments,
  runActive = false,
  now,
}: {
  project: Project;
  /** Newest first, as the API lists them. */
  versions: readonly TimelineVersionSummary[];
  comments: CommentsStore;
  /** An editor run is working: with no version yet, the screen says the first cut is on its way. */
  runActive?: boolean;
  now?: number;
}) {
  const latest = versions.reduce((max, version) => Math.max(max, version.version), 0);
  const [shown, setShown] = React.useState<number | undefined>(undefined);
  const selected = shown ?? (latest || undefined);

  const specs = useTimelineSpecs(project.id, versions);
  const preview = usePreviewRender(project.id, selected);
  const renderState = preview.state;
  const readyRender = renderState.kind === "ready" ? renderState.render : undefined;
  const renderTimeMap = useRenderTimeMap(readyRender?.id, preview.timeMapUrl);

  const titles = React.useMemo(() => new Map(project.assets.map((asset) => [asset.asset_id as string, asset.title])), [project.assets]);
  const assetNames = React.useMemo(() => Object.fromEntries(titles), [titles]);

  const reviewVersions = specs.map((query, index) => {
    const summary = versions[index] as TimelineVersionSummary;
    const isShown = summary.version === selected;
    return toReviewVersion(
      {
        summary,
        version: query.data ?? null,
        ...(isShown && readyRender ? { render: readyRender, previewUrl: renderState.kind === "ready" ? renderState.mp4Url : undefined, renderTimeMap } : {}),
      },
      project.format as OutputFormat,
    );
  });
  const ready = reviewVersions.flatMap((version) => version ?? []).sort((a, b) => a.version - b.version);
  const loading = specs.some((query) => query.isPending && query.fetchStatus !== "idle");
  const failed = !loading && ready.length === 0 && versions.length > 0;

  const initial = React.useMemo(() => comments.list(project.id), [comments, project.id]);
  const onCommentCreate = React.useCallback((comment: ReviewAnnotation) => comments.add(project.id, comment), [comments, project.id]);

  if (versions.length === 0) {
    return (
      <section aria-label="Review" className="grid place-items-center rounded-xl bg-surface-raised px-6 py-16 text-center shadow-soft ring-1 ring-border">
        <div className="max-w-sm space-y-2">
          {runActive ? <Loader2 aria-hidden className="mx-auto size-6 text-brand motion-safe:animate-spin" /> : <Film aria-hidden className="mx-auto size-6 text-muted-foreground" />}
          <h2 className="text-[15px] font-semibold tracking-[-0.01em]">{runActive ? "Your first cut is on its way" : "No cut yet"}</h2>
          <p className="text-[13px] text-muted-foreground text-pretty">
            {runActive ? "The editor is looking at your footage. The cut shows up here as soon as it has one." : "Start the editor below to make the first cut from your footage."}
          </p>
        </div>
      </section>
    );
  }
  if (loading) {
    return (
      <section aria-label="Review" aria-busy="true" className="space-y-3">
        <Skeleton className="h-9 w-64" />
        <Skeleton className="h-96 w-full rounded-xl" />
        <Skeleton className="h-28 w-full rounded-xl" />
      </section>
    );
  }
  if (failed) {
    return (
      <p role="alert" className="rounded-xl bg-del-wash px-4 py-3 text-destructive">
        The versions of this project could not be loaded. Reload the page to try again.
      </p>
    );
  }

  const requestError = preview.request.isError ? renderMessage(preview.request.error) : undefined;

  return (
    <ReviewScreen
      // A new latest version (a run finished) reopens the screen on it; the comments come back from the store.
      key={latest}
      versions={ready}
      comments={initial}
      assetNames={assetNames}
      localVideo={false}
      placeholderLabel={renderState.kind === "ready" ? "Preview loading" : "No video preview yet"}
      commentsNote={COMMENTS_NOT_SAVED_NOTE}
      onCommentCreate={onCommentCreate}
      onVersionChange={setShown}
      onVideoError={preview.refreshUrls}
      aboveVideo={({ version }) => (
        <PreviewStatus
          state={renderState}
          version={version.version}
          onRequest={() => preview.request.mutate()}
          requesting={preview.request.isPending}
          requestError={requestError}
        />
      )}
      belowTimeline={({ version, selectVersion }) => <VersionHistory versions={versions} selected={version.version} onSelect={selectVersion} now={now} />}
    />
  );
}
