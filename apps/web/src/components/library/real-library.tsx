import { Button } from "@/components/ui/button";
import { notify } from "@/components/ui/toast";
import type { ApiAsset } from "@/lib/api/client";
import { useDeleteAsset } from "@/lib/api/hooks";
import type { UploadItem } from "@/lib/upload/manager";
import { useUploadCommands, useUploadSelector } from "@/lib/upload/provider";
import { LibraryGrid } from "./library-grid";
import type { Asset, AssetId } from "./types";

/**
 * An asset of the API as the library shows it. The API does not know duration, shape or a poster yet (ingest, P1-04),
 * so those are placeholders: a 16:9 card without a duration. An asset that is uploading shows the live progress of this
 * browser's upload when there is one, and a stopped upload shows as failed with a Retry that continues it.
 */
export function toLibraryAsset(asset: ApiAsset, local?: LocalUpload): Asset {
  const uploading = asset.status === "uploading";
  const stalled = uploading && local?.status === "failed";
  return {
    id: asset.id as AssetId,
    title: asset.title || asset.filename,
    description: asset.description,
    tags: asset.tags,
    status: stalled ? "failed" : asset.status,
    progress: uploading && local && !stalled ? local.percent : undefined,
    durationMs: 0,
    width: 16,
    height: 9,
    createdAt: asset.created_at,
    retryable: stalled,
  };
}

/** What the library needs to know of this browser's upload of an asset: no more, so that it renders when a percent changes and not on every byte. */
export interface LocalUpload {
  id: string;
  assetId: string;
  status: UploadItem["status"];
  percent: number;
}

const sameUploads = (a: readonly LocalUpload[], b: readonly LocalUpload[]) =>
  a.length === b.length && a.every((x, i) => x.id === b[i]?.id && x.status === b[i]?.status && x.percent === b[i]?.percent && x.assetId === b[i]?.assetId);

const selectLocalUploads = (snapshot: { items: readonly UploadItem[] }): LocalUpload[] =>
  snapshot.items.flatMap((item) => (item.assetId ? [{ id: item.id, assetId: item.assetId, status: item.status, percent: Math.round(item.progress * 100) }] : []));

/** The library on the workspace's real assets. */
export function RealLibrary({ assets, loading, hasMore, loadingMore, onLoadMore }: { assets: readonly ApiAsset[]; loading?: boolean; hasMore?: boolean; loadingMore?: boolean; onLoadMore?: () => void }) {
  const { cancel, retry } = useUploadCommands();
  const uploads = useUploadSelector(selectLocalUploads, sameUploads);
  const deleteAsset = useDeleteAsset();
  const byAsset = new Map(uploads.map((upload) => [upload.assetId, upload]));
  const shown = assets.map((asset) => toLibraryAsset(asset, byAsset.get(asset.id)));

  return (
    <div className="space-y-4">
      <LibraryGrid
        assets={shown}
        loading={loading}
        onDelete={(asset) => {
          // An upload of this browser is stopped here and the asset is deleted on the server, which is what the creator asked for.
          const local = byAsset.get(asset.id);
          if (local) cancel(local.id, { abortOnServer: false });
          deleteAsset.mutate(asset.id, { onError: () => notify("The clip could not be deleted", { description: "Try again in a moment.", type: "error" }) });
        }}
        onRetry={(asset) => {
          const local = byAsset.get(asset.id);
          if (local) retry(local.id);
        }}
      />
      {hasMore ? (
        <div className="flex justify-center">
          <Button variant="secondary" disabled={loadingMore} onClick={onLoadMore}>
            {loadingMore ? "Loading" : "Show more clips"}
          </Button>
        </div>
      ) : null}
    </div>
  );
}
