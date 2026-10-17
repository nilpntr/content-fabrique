import { useQueryClient } from "@tanstack/react-query";
import * as React from "react";
import { Button } from "@/components/ui/button";
import { notify } from "@/components/ui/toast";
import { type ApiAsset, fetchSpriteVtt } from "@/lib/api/client";
import { useDeleteAsset } from "@/lib/api/hooks";
import { useApiClient } from "@/lib/api/provider";
import { ASSETS_QUERY_KEY } from "@/lib/query-client";
import type { UploadItem } from "@/lib/upload/manager";
import { useUploadCommands, useUploadSelector } from "@/lib/upload/provider";
import { LibraryGrid } from "./library-grid";
import type { Asset, AssetId, Sprite } from "./types";

/** Why an asset failed, in words for the creator. The codes are the API's `failure_reason`; an unknown one gets the general line. */
const FAILURE_MESSAGES: Readonly<Record<string, string>> = {
  upload_expired: "The upload took too long and was cancelled. Upload the clip again.",
  upload_aborted: "The upload was cancelled.",
  size_mismatch: "The file changed while it was uploading. Upload it again.",
  unsupported_format: "This file format is not supported.",
  corrupt_media: "The file is damaged or was cut off. Try exporting it again.",
  no_video_stream: "There is no video in this file.",
  no_audio_stream: "There is no audio in this file.",
  undecodable_video: "The video in this file cannot be read.",
  invalid_duration: "The length of this file could not be read.",
  too_long: "Clips can be 30 minutes at most.",
  unsupported_resolution: "The resolution of this clip is not supported.",
  kind_mismatch: "The file is not the kind of media its type says.",
  processing_failed: "We could not process this clip. Try uploading it again.",
  processing_timeout: "Processing this clip took too long. Try uploading it again.",
  processing_interrupted: "Processing was interrupted. Try uploading the clip again.",
};

export function failureMessage(reason: string | null): string | undefined {
  if (reason === null) return undefined;
  return FAILURE_MESSAGES[reason] ?? "This clip could not be added.";
}

const STAGES: Readonly<Record<string, string>> = {
  starting: "Starting",
  downloading: "Downloading the original",
  probing: "Reading the file",
  proxy: "Making the preview video",
  derivatives: "Making thumbnails",
  uploading: "Saving the results",
};

/** What the library needs to know of this browser's upload of an asset: no more, so that it renders when a percent changes and not on every byte. */
export interface LocalUpload {
  id: string;
  assetId: string;
  status: UploadItem["status"];
  percent: number;
}

/**
 * An asset of the API as the library shows it:
 *  - an upload in progress shows this browser's live progress; one that stopped shows as failed, with a Retry that continues it;
 *  - a clip being ingested shows the ingest's progress, and the stage as a tooltip;
 *  - a ready clip has its duration, its upright size (the probe already applied the rotation, so a phone clip is portrait),
 *    its poster, its hover preview and its scrub sprite. The sprite's cues come from `GET /v1/assets/{id}/sprite.vtt`
 *    (via `loadVtt`), because the stored track names the sheet relatively;
 *  - a failed clip says why.
 * The media URLs are presigned and expire (`derivative_urls_expire_at`); RealLibrary fetches the list again before that.
 */
export function toLibraryAsset(asset: ApiAsset, local?: LocalUpload, loadVtt?: (id: string) => Promise<string>): Asset {
  const uploading = asset.status === "uploading";
  const stalled = uploading && local?.status === "failed";
  const status = stalled ? "failed" : asset.status;
  const media = asset.media;
  const derivatives = asset.derivatives;
  let sprite: Sprite | undefined;
  if (derivatives?.sprite && derivatives.sprite_vtt && loadVtt) {
    const id = asset.id;
    sprite = { url: derivatives.sprite.url, vttKey: `${id}:${asset.derivative_urls_expire_at ?? ""}`, loadVtt: () => loadVtt(id) };
  }
  return {
    id: asset.id as AssetId,
    title: asset.title || asset.filename,
    description: asset.description,
    tags: asset.tags,
    status,
    progress: uploading && local && !stalled ? local.percent : asset.status === "processing" ? asset.ingest_progress : undefined,
    stage: asset.status === "processing" && asset.ingest_stage ? (STAGES[asset.ingest_stage] ?? asset.ingest_stage) : undefined,
    durationMs: media?.duration_ms ?? 0,
    // Upright size from the probe; an audio file or a clip not ingested yet is a 16:9 placeholder.
    width: media?.width ?? 16,
    height: media?.height ?? 9,
    createdAt: asset.created_at,
    posterUrl: derivatives?.poster?.url,
    hoverPreviewUrl: derivatives?.hover_preview?.url,
    sprite,
    mediaExpiresAt: asset.derivative_urls_expire_at,
    retryable: stalled,
    failureMessage: status === "failed" && !stalled ? failureMessage(asset.failure_reason) : undefined,
  };
}

const sameUploads = (a: readonly LocalUpload[], b: readonly LocalUpload[]) =>
  a.length === b.length && a.every((x, i) => x.id === b[i]?.id && x.status === b[i]?.status && x.percent === b[i]?.percent && x.assetId === b[i]?.assetId);

const selectLocalUploads = (snapshot: { items: readonly UploadItem[] }): LocalUpload[] =>
  snapshot.items.flatMap((item) => (item.assetId ? [{ id: item.id, assetId: item.assetId, status: item.status, percent: Math.round(item.progress * 100) }] : []));

/** How soon the list may be fetched again because a picture or video would not load. */
const MEDIA_REFRESH_MIN_MS = 10_000;
/** The URLs are renewed this long before they expire. */
const EXPIRY_MARGIN_MS = 60_000;

/** The library on the workspace's real assets. */
export function RealLibrary({ assets, loading, hasMore, loadingMore, onLoadMore }: { assets: readonly ApiAsset[]; loading?: boolean; hasMore?: boolean; loadingMore?: boolean; onLoadMore?: () => void }) {
  const { cancel, retry } = useUploadCommands();
  const uploads = useUploadSelector(selectLocalUploads, sameUploads);
  const deleteAsset = useDeleteAsset();
  const getClient = useApiClient();
  const queryClient = useQueryClient();
  const loadVtt = React.useCallback((id: string) => fetchSpriteVtt(getClient(), id), [getClient]);
  const shown = React.useMemo(() => {
    const byAsset = new Map(uploads.map((upload) => [upload.assetId, upload]));
    return assets.map((asset) => toLibraryAsset(asset, byAsset.get(asset.id), loadVtt));
  }, [assets, uploads, loadVtt]);
  const localOf = (id: string) => uploads.find((upload) => upload.assetId === id);

  // A picture or video that does not load has an expired URL (they last 30 minutes), so the list, which carries fresh
  // ones, is fetched again; at most every few seconds, however many cards fail together.
  const lastRefresh = React.useRef(0);
  const refresh = React.useCallback(() => {
    const now = Date.now();
    if (now - lastRefresh.current < MEDIA_REFRESH_MIN_MS) return;
    lastRefresh.current = now;
    void queryClient.invalidateQueries({ queryKey: [ASSETS_QUERY_KEY] });
  }, [queryClient]);

  // And before they expire, so that a page left open for an hour keeps its previews.
  React.useEffect(() => {
    const expiries = assets.flatMap((asset) => (asset.derivative_urls_expire_at ? [Date.parse(asset.derivative_urls_expire_at)] : [])).filter(Number.isFinite);
    if (expiries.length === 0) return;
    const wait = Math.min(2 ** 31 - 1, Math.max(MEDIA_REFRESH_MIN_MS, Math.min(...expiries) - Date.now() - EXPIRY_MARGIN_MS));
    const timer = setTimeout(() => {
      lastRefresh.current = 0;
      refresh();
    }, wait);
    return () => clearTimeout(timer);
  }, [assets, refresh]);

  return (
    <div className="space-y-4">
      <LibraryGrid
        assets={shown}
        loading={loading}
        onMediaError={refresh}
        onDelete={(asset) => {
          // An upload of this browser is stopped here and the asset is deleted on the server, which is what the creator asked for.
          const local = localOf(asset.id);
          if (local) cancel(local.id, { abortOnServer: false });
          deleteAsset.mutate(asset.id, { onError: () => notify("The clip could not be deleted", { description: "Try again in a moment.", type: "error" }) });
        }}
        onRetry={(asset) => {
          const local = localOf(asset.id);
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
