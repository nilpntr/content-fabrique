import { FolderOpen } from "lucide-react";
import * as React from "react";
import type { Asset } from "./types";

/**
 * Local manual testing only. `?samples=local` shows a file input; the videos you pick are played through object URLs
 * as the hover previews of the sample clips. Nothing is uploaded, nothing is stored, and no footage is in the repo.
 */
export function isLocalSamples(search: string): boolean {
  return new URLSearchParams(search).get("samples") === "local";
}

/** Gives the ready clips a hover preview from `urls`, round-robin. Without urls the assets come back unchanged. */
export function withLocalPreviews(assets: readonly Asset[], urls: readonly string[]): Asset[] {
  let next = 0;
  return assets.map((asset) => {
    if (asset.status !== "ready" || urls.length === 0) return asset;
    const hoverPreviewUrl = urls[next % urls.length];
    next++;
    return { ...asset, hoverPreviewUrl };
  });
}

export function LocalSamplesPicker({ onUrls }: { onUrls: (urls: string[]) => void }) {
  const urls = React.useRef<string[]>([]);
  const onUrlsRef = React.useRef(onUrls);
  onUrlsRef.current = onUrls;

  React.useEffect(
    () => () => {
      for (const url of urls.current) URL.revokeObjectURL(url);
    },
    [],
  );

  return (
    <label className="flex w-fit cursor-pointer items-center gap-2 rounded-[10px] bg-wash px-3 py-2 text-[13px] text-muted-foreground shadow-[inset_0_0_0_1px_var(--border)] hover:text-foreground">
      <FolderOpen aria-hidden="true" className="size-4" />
      Pick local videos to use as hover previews
      <input
        type="file"
        accept="video/*"
        multiple
        className="sr-only"
        onChange={(event) => {
          for (const url of urls.current) URL.revokeObjectURL(url);
          urls.current = Array.from(event.target.files ?? [], (file) => URL.createObjectURL(file));
          onUrlsRef.current(urls.current);
        }}
      />
    </label>
  );
}
