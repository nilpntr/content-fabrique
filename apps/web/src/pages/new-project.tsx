import { useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { FilmIcon } from "lucide-react";
import * as React from "react";
import { NewProjectFlow } from "@/components/new-project/new-project-flow";
import type { Asset, AssetId, NewProjectInput } from "@/components/new-project/types";
import { DEFAULT_BUDGET_USD } from "@/components/project/edit-again";
import { createProjectMessage, startEditMessage } from "@/components/project/errors";
import { Button, buttonVariants } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { notify } from "@/components/ui/toast";
import type { ApiAsset } from "@/lib/api/client";
import { useAssets } from "@/lib/api/hooks";
import { useCreateProject, useStartEdit } from "@/lib/api/project-hooks";
import { ASSETS_QUERY_KEY } from "@/lib/query-client";

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

const MEDIA_REFRESH_MIN_MS = 10_000;

/**
 * Where a project starts: the ready videos of the library to pick from, the format and the instructions. Creating makes the
 * project, starts the editor on it with the instructions, and opens the project page, which follows the run.
 */
export function NewProjectPage({ onOpenProject }: { onOpenProject: (projectId: string, runId?: string) => void }) {
  const assets = useAssets();
  const { hasNextPage, isFetchingNextPage, fetchNextPage } = assets;
  const createProject = useCreateProject();
  const startEdit = useStartEdit();
  const queryClient = useQueryClient();

  // The picker needs the whole library to search it, so the remaining pages are fetched as they come.
  React.useEffect(() => {
    if (hasNextPage && !isFetchingNextPage) void fetchNextPage();
  }, [hasNextPage, isFetchingNextPage, fetchNextPage]);

  const clips = React.useMemo(() => (assets.data ?? []).filter(isUsableClip).map(toPickerAsset), [assets.data]);
  const loading = assets.isPending && assets.fetchStatus !== "idle";

  // A poster that does not load has an expired URL (they last 30 minutes): fetch the list again, at most every few seconds.
  const lastRefresh = React.useRef(0);
  const refresh = React.useCallback(() => {
    const now = Date.now();
    if (now - lastRefresh.current < MEDIA_REFRESH_MIN_MS) return;
    lastRefresh.current = now;
    void queryClient.invalidateQueries({ queryKey: [ASSETS_QUERY_KEY] });
  }, [queryClient]);

  async function onCreate(input: NewProjectInput) {
    const project = await createProject.mutateAsync({
      format: input.format,
      asset_ids: input.assetIds,
      ...(input.title ? { title: input.title } : {}),
      ...(input.brief ? { brief: input.brief } : {}),
    });
    try {
      const run = await startEdit.mutateAsync({ projectId: project.id, body: input.brief ? { instructions: input.brief } : {} });
      onOpenProject(project.id, run.id);
    } catch (error) {
      // The project exists; only the editor did not start. Its page offers "Start the editor".
      notify("The project was created, but the editor did not start", { description: startEditMessage(error), type: "error" });
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
        <h1 className="text-lg font-semibold tracking-[-0.02em]">{processing ? "Your clips are still processing" : "Upload some clips first"}</h1>
        <p className="max-w-sm text-muted-foreground text-pretty">
          {processing
            ? "A project can use a clip once it has been processed. They show up here by themselves."
            : "A project is made from your own footage. Add a few videos to the library, then come back."}
        </p>
        <Link to="/library" search={{ state: undefined, samples: undefined }} className={buttonVariants({ size: "md" })}>
          Go to the library
        </Link>
      </div>
    );
  }

  return <NewProjectFlow assets={clips} onCreate={onCreate} onMediaError={refresh} budgetUsd={DEFAULT_BUDGET_USD} describeError={describeError} />;
}
