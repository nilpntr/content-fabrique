import * as React from "react";
import { LibraryGrid } from "./library-grid";
import { isLocalSamples, LocalSamplesPicker, withLocalPreviews } from "./local-samples";
import { SAMPLE_ASSETS } from "./sample-data";
import type { Asset } from "./types";

/**
 * The library on sample data, for the page that mounts it before the API exists. `?samples=local` adds the local
 * video picker; `?state=loading` and `?state=empty` show those two states.
 */
export function LibraryDemo({ search = typeof window === "undefined" ? "" : window.location.search }: { search?: string }) {
  const [assets, setAssets] = React.useState<Asset[]>(SAMPLE_ASSETS);
  const [urls, setUrls] = React.useState<string[]>([]);
  const state = new URLSearchParams(search).get("state");
  const shown = React.useMemo(() => withLocalPreviews(assets, urls), [assets, urls]);

  return (
    <div className="space-y-4">
      {isLocalSamples(search) ? <LocalSamplesPicker onUrls={setUrls} /> : null}
      <LibraryGrid
        assets={state === "empty" ? [] : shown}
        loading={state === "loading"}
        onDelete={(asset) => setAssets((current) => current.filter((item) => item.id !== asset.id))}
        onRetry={(asset) => setAssets((current) => current.map((item) => (item.id === asset.id ? { ...item, status: "processing", progress: undefined } : item)))}
        onUpload={() => {}}
      />
    </div>
  );
}
