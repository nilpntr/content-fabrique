import { useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { FilmIcon } from "lucide-react";
import * as React from "react";
import { NewProjectFlow } from "@/components/new-project/new-project-flow";
import type { Asset, AssetId, NewProjectInput } from "@/components/new-project/types";
import { extraCarefulCosts } from "@/components/extra-careful";
import { createProjectMessage, startEditMessage } from "@/components/project/errors";
import { Button, buttonVariants } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { notify } from "@/components/ui/toast";
import type { ApiAsset } from "@/lib/api/client";
import { useAssets, useMe } from "@/lib/api/hooks";
import { useCreateProject, useStartEdit } from "@/lib/api/project-hooks";
import { creditsOf } from "@/lib/credits";
import { ASSETS_QUERY_KEY } from "@/lib/query-client";
import { useRefreshThrottle } from "@/lib/refresh-throttle";

/** A clip the editor can use: a video that has finished processing. */
export const isUsableClip = (asset: ApiAsset): boolean => asset.kind === "video" && asset.status === "ready";

/** A library asset as the picker shows it. */
export function toPickerAsset(asset: ApiAsset): Asset {
  return {
    id: asset.id as AssetId,
    title: asset.title || asset.filename,
    tags: asset.tags,
    durationMs: asset.media?.duration_ms ?? 0,
    posterUrl: asset.derivatives?.poster?.url,
    hoverPreviewUrl: asset.derivatives?.hover_preview?.url,
  };
}

/**
 * Where a project starts: the ready videos of the library to pick from, the format and the instructions. Creating makes the
 * project, starts the editor on it with the instructions, and opens the project page, which follows the run.
 */
export function NewProjectPage({ onOpenProject }: { onOpenProject: (projectId: string, runId?: string) => void }) {
  const assets = useAssets();
  const { hasNextPage, isFetchingNextPage, isFetchNextPageError, fetchNextPage } = assets;
  const createProject = useCreateProject();
  const startEdit = useStartEdit();
  const me = useMe();
  const extraCareful = extraCarefulCosts(creditsOf(me.data)?.edit_costs, "first_edit");
  const queryClient = useQueryClient();

  // The picker needs the whole library to search it, so the remaining pages are fetched as they come. After a failure it stops,
  // and a button asks again: a loop of retries against a failing API helps nobody.
  React.useEffect(() => {
    if (hasNextPage && !isFetchingNextPage && !isFetchNextPageError) void fetchNextPage();
  }, [hasNextPage, isFetchingNextPage, isFetchNextPageError, fetchNextPage]);

  const clips = React.useMemo(() => (assets.data ?? []).filter(isUsableClip).map(toPickerAsset), [assets.data]);
  // Pending includes "not asked yet" (the session is still loading): that is loading, not an empty library.
  const loading = assets.isPending;

  // A poster that does not load has an expired URL (they last 30 minutes): fetch the list again, at most every few seconds.
  const { refresh } = useRefreshThrottle(() => void queryClient.invalidateQueries({ queryKey: [ASSETS_QUERY_KEY] }));

  async function onCreate(input: NewProjectInput) {
    const project = await createProject.mutateAsync({
      format: input.format,
      asset_ids: input.assetIds,
      ...(input.title ? { title: input.title } : {}),
      ...(input.brief ? { brief: input.brief } : {}),
    });
    try {
      const run = await startEdit.mutateAsync({
        projectId: project.id,
        body: { ...(input.brief ? { instructions: input.brief } : {}), ...(input.quality ? { quality: input.quality } : {}) },
      });
      onOpenProject(project.id, run.id);
    } catch (error) {
      // The project exists; only the editor did not start. Its page offers "Start the editor".
      notify("Your project is saved, but I couldn't start editing", { description: startEditMessage(error), type: "error" });
      onOpenProject(project.id);
    }
  }

  function describeError(error: unknown) {
    // A clip that vanished or was not usable: the library is probably not what the picker shows.
    void queryClient.invalidateQueries({ queryKey: [ASSETS_QUERY_KEY] });
    return createProjectMessage(error);
  }

  if (loading) {
    return (
      <div className="space-y-6" aria-busy="true">
        <Skeleton className="h-8 w-56" />
        <Skeleton className="h-64 w-full rounded-xl" />
        <p role="status" className="sr-only">
          Loading your clips
        </p>
      </div>
    );
  }

  if (assets.isError && clips.length === 0) {
    return (
      <div role="alert" className="mx-auto max-w-xl space-y-2 rounded-2xl bg-well py-12 text-center shadow-well">
        <p className="font-medium">Your clips could not be loaded</p>
        <p className="text-muted-foreground">Check your connection and try again.</p>
        <Button variant="secondary" size="sm" onClick={() => void assets.refetch()}>
          Try again
        </Button>
      </div>
    );
  }

  if (clips.length === 0) {
    const processing = (assets.data ?? []).some((asset) => asset.kind === "video" && asset.status !== "ready" && asset.status !== "failed");
    return (
      <div className="mx-auto flex max-w-xl flex-col items-center gap-3 rounded-2xl bg-well py-16 text-center shadow-well">
        <FilmIcon aria-hidden className="size-6 text-muted-foreground" />
        <h1 className="text-lg font-semibold tracking-[-0.02em]">{processing ? "Your clips are almost ready" : "Upload some clips first"}</h1>
        <p className="max-w-sm text-muted-foreground text-pretty">
          {processing
            ? "I'm still getting your clips ready. They show up here by themselves in a minute or two."
            : "Your video is made from your own footage. Add a few clips to your library, then come back."}
        </p>
        <Link to="/library" search={{ state: undefined, samples: undefined }} className={buttonVariants({ size: "md" })}>
          Upload clips
        </Link>
      </div>
    );
  }

  return (
    <div className="space-y-4">
      {isFetchNextPageError ? (
        <div role="alert" className="flex flex-wrap items-center gap-3 rounded-xl bg-del-wash px-3 py-2 text-[13.5px]">
          <p className="text-destructive">Some of your clips could not be loaded.</p>
          <Button variant="secondary" size="sm" disabled={isFetchingNextPage} onClick={() => void fetchNextPage()}>
            {isFetchingNextPage ? "Loading…" : "Load the rest"}
          </Button>
        </div>
      ) : null}
      <NewProjectFlow assets={clips} onCreate={onCreate} onMediaError={refresh} costNote="About one video from your plan, with room for two rounds of changes." describeError={describeError} extraCareful={extraCareful} />
    </div>
  );
}
