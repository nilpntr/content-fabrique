import { type DemoState, LibraryDemo } from "@/components/library/library-demo";
import { LibraryGrid } from "@/components/library/library-grid";
import { RealLibrary } from "@/components/library/real-library";
import { PageHeader } from "@/components/page";
import { Button } from "@/components/ui/button";
import { ResumePrompt } from "@/components/uploads/uploads-tray";
import { UploadDropzone } from "@/components/uploads/upload-dropzone";
import { useAssets } from "@/lib/api/hooks";
import { useUploadSelector } from "@/lib/upload/provider";

/**
 * The library: the workspace's clips from the API, with the upload zone above them. With none yet it says how to add the
 * first ones; it never shows made-up clips to a creator. `?state=` and `?samples=local` show the sample clips: they are for
 * trying the grid by hand.
 */
export function LibraryPage({
  state,
  localSamples,
  openAssetId,
  onOpenAsset,
}: {
  state?: DemoState;
  localSamples?: boolean;
  /** The clip whose sheet is open (`?open=`), and how to change it. Without them the page keeps this itself. */
  openAssetId?: string | undefined;
  onOpenAsset?: (assetId: string | null) => void;
}) {
  const forceSamples = state !== undefined || Boolean(localSamples);
  // Assets that this browser is uploading report their own progress: the list does not poll for them.
  const owned = useUploadSelector((snapshot) => snapshot.items.flatMap((item) => (item.assetId && item.status !== "done" ? [item.assetId] : [])).join(","));
  const assets = useAssets({ enabled: !forceSamples, isOwned: (id) => owned.split(",").includes(id) });
  const real = !forceSamples && (assets.data?.length ?? 0) > 0;
  const loading = !forceSamples && assets.isPending && assets.fetchStatus !== "idle";

  return (
    <div className="space-y-6">
      <PageHeader title="Library" description="All your clips in one place. Hover a clip to preview it, or tap it to see what's in it." />
      <ResumePrompt />
      <UploadDropzone />
      {forceSamples ? (
        <LibraryDemo state={state} localSamples={localSamples} />
      ) : assets.isError && !real ? (
        <div role="alert" className="max-w-xl space-y-2 rounded-2xl bg-well py-10 text-center shadow-well">
          <p className="font-medium">Your clips could not be loaded</p>
          <p className="text-muted-foreground">Check your connection and try again.</p>
          <Button variant="secondary" size="sm" onClick={() => void assets.refetch()}>
            Try again
          </Button>
        </div>
      ) : real || loading ? (
        <RealLibrary
          assets={assets.data ?? []}
          loading={loading}
          openId={onOpenAsset ? (openAssetId ?? null) : undefined}
          onOpenChange={onOpenAsset}
          hasMore={assets.hasNextPage}
          loadingMore={assets.isFetchingNextPage}
          onLoadMore={() => void assets.fetchNextPage()}
        />
      ) : (
        <LibraryGrid assets={[]} />
      )}
    </div>
  );
}
