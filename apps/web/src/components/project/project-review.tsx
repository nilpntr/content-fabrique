import { CircleAlert, Film, Loader2 } from "lucide-react";
import * as React from "react";
import { ReviewScreen } from "@/components/editor/review-screen";
import type { OutputFormat, ReviewAnnotation, ReviewVersion } from "@/components/editor/types";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import type { Project, TimelineVersionSummary } from "@/lib/api/client";
import { usePreviewRender, useRenderTimeMap, useTimelineSpecs } from "@/lib/api/project-hooks";
import { COMMENTS_NOT_SAVED_NOTE, type CommentsStore } from "./comments-store";
import { renderMessage } from "./errors";
import { PreviewStatus } from "./preview-status";
import { loadingReviewVersion, toReviewVersion } from "./review-data";
import { VersionHistory } from "./version-history";

/**
 * The review screen on a project's real data: the versions of its timeline (each version's spec gives the cut marks and the
 * labels; the asset titles name the clips), the preview render of the version on screen as the video, and comments that live
 * in `comments` (in memory for now).
 *
 * Specs are fetched lazily: the latest version, the one on screen, and the ones that have comments (a comment is moved
 * between versions through their time maps). The other versions are listed and can be chosen, and show as loading until their
 * spec arrives; a spec never changes once fetched, so it is kept.
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

  // Read from the store on every render, so that a screen that opens again starts from what was written.
  const stored = comments.list(project.id);
  const wanted = new Set<number>([latest, ...(selected ? [selected] : []), ...stored.map((comment) => comment.version)]);
  const needed = versions.filter((version) => wanted.has(version.version));
  const specs = useTimelineSpecs(project.id, needed);
  const specOf = new Map(needed.map((summary, index) => [summary.version, specs[index]]));

  const preview = usePreviewRender(project.id, selected);
  const renderState = preview.state;
  const readyRender = renderState.kind === "ready" ? renderState.render : undefined;
  const renderTimeMap = useRenderTimeMap(readyRender?.id, preview.timeMapUrl);

  const assetNames = React.useMemo(() => Object.fromEntries(project.assets.map((asset) => [asset.asset_id as string, asset.title])), [project.assets]);

  const format = project.format as OutputFormat;
  const reviewVersions: ReviewVersion[] = [...versions]
    .sort((a, b) => a.version - b.version)
    .map((summary) => {
      const spec = specOf.get(summary.version)?.data;
      const isShown = summary.version === selected;
      const full = toReviewVersion(
        { summary, version: spec ?? null, ...(isShown && readyRender ? { render: readyRender, previewUrl: renderState.kind === "ready" ? renderState.mp4Url : undefined, renderTimeMap } : {}) },
        format,
      );
      return full ?? loadingReviewVersion(summary, format);
    });
  const loadedLatest = reviewVersions.reduce((max, version) => (version.loading ? max : Math.max(max, version.version)), 0);
  const firstLoad = loadedLatest === 0 && specs.some((query) => query.isPending);
  const failed = loadedLatest === 0 && !firstLoad && versions.length > 0;

  const onCommentsChange = React.useCallback((next: ReviewAnnotation[]) => comments.replace(project.id, next), [comments, project.id]);

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
  // Only the very first load swaps the screen for a skeleton; later, a version that is still loading says so inside the screen.
  if (firstLoad) {
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

  const requestError = preview.requestError ? renderMessage(preview.requestError) : undefined;

  return (
    <ReviewScreen
      // The screen reopens on a newer latest version once its spec is there (the comments come back from the store); until then it stays where it is.
      key={loadedLatest}
      versions={reviewVersions}
      initialVersion={loadedLatest}
      comments={stored}
      assetNames={assetNames}
      localVideo={false}
      placeholderLabel={renderState.kind === "ready" ? "Preview loading" : "No video preview yet"}
      commentsNote={COMMENTS_NOT_SAVED_NOTE}
      onCommentsChange={onCommentsChange}
      onVersionChange={setShown}
      onVideoError={preview.refreshUrls}
      aboveVideo={({ version }) => {
        if (version.loading) {
          const query = specOf.get(version.version);
          return query?.isError ? (
            <div role="alert" className="flex flex-wrap items-center gap-3 rounded-[10px] bg-del-wash px-3 py-2 text-[12.5px] text-destructive">
              <CircleAlert aria-hidden className="size-3.5 shrink-0" />
              v{version.version} could not be loaded.
              <Button size="sm" variant="secondary" className="ml-auto" onClick={() => void query.refetch()}>
                Try again
              </Button>
            </div>
          ) : (
            <p role="status" className="flex items-center gap-2 rounded-[10px] bg-wash px-3 py-2 text-[12.5px] text-muted-foreground shadow-[inset_0_0_0_1px_var(--border)]">
              <Loader2 aria-hidden className="size-3.5 motion-safe:animate-spin" />
              Loading v{version.version}…
            </p>
          );
        }
        return (
          <PreviewStatus state={renderState} version={version.version} onRequest={preview.requestRender} requesting={preview.requesting} requestError={requestError} />
        );
      }}
      belowTimeline={({ version, selectVersion }) => <VersionHistory versions={versions} selected={version.version} onSelect={selectVersion} now={now} />}
    />
  );
}
