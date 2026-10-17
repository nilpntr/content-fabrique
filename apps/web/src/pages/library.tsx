import { Info } from "lucide-react";
import { type DemoState, LibraryDemo } from "@/components/library/library-demo";
import { RealLibrary } from "@/components/library/real-library";
import { ResumePrompt } from "@/components/uploads/uploads-tray";
import { UploadDropzone } from "@/components/uploads/upload-dropzone";
import { useAssets } from "@/lib/api/hooks";

/**
 * The library: the workspace's real assets from the API, with the upload zone above them. Until the workspace has any
 * (or when the API cannot be asked), it shows the sample clips with a notice, so that the page is never an empty box.
 * `?state=` and `?samples=local` always show the samples: they are for trying the grid by hand.
 */
export function LibraryPage({ state, localSamples }: { state?: DemoState; localSamples?: boolean }) {
  const forceSamples = state !== undefined || Boolean(localSamples);
  const assets = useAssets({ enabled: !forceSamples });
  const real = !forceSamples && (assets.data?.length ?? 0) > 0;
  const loading = !forceSamples && assets.isPending && assets.fetchStatus !== "idle";

  return (
    <div className="space-y-6">
      <div className="animate-rise">
        <h1 className="text-[1.625rem] leading-tight font-semibold tracking-[-0.025em] text-balance">Library</h1>
        <p className="mt-1.5 max-w-prose text-muted-foreground">Your footage. Hover a clip to preview it, and move across it to scrub.</p>
      </div>
      <ResumePrompt />
      <UploadDropzone />
      {real || loading ? null : (
        <p role="note" className="flex w-fit items-center gap-2 rounded-[10px] bg-wash px-3 py-2 text-[12.5px] text-muted-foreground shadow-[inset_0_0_0_1px_var(--border)]">
          <Info aria-hidden="true" className="size-4 shrink-0 text-subtle-foreground" />
          Sample clips until you upload your own
        </p>
      )}
      {real || loading ? (
        <RealLibrary
          assets={assets.data ?? []}
          loading={loading}
          hasMore={assets.hasNextPage}
          loadingMore={assets.isFetchingNextPage}
          onLoadMore={() => void assets.fetchNextPage()}
        />
      ) : (
        <LibraryDemo state={state} localSamples={localSamples} />
      )}
    </div>
  );
}
