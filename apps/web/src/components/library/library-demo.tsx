import * as React from "react";
import { LibraryGrid } from "./library-grid";
import { LocalSamplesPicker, withLocalPreviews } from "./local-samples";
import { SAMPLE_ASSETS } from "./sample-data";
import type { Asset } from "./types";

export type DemoState = "loading" | "empty";

/** `?state=` of the library page: the two states that are hard to reach on sample data. Anything else is ignored. */
export function parseDemoState(value: unknown): DemoState | undefined {
  return value === "loading" || value === "empty" ? value : undefined;
}

/**
 * The library on sample data, until the API exists. `localSamples` adds a picker for your own videos, used as the
 * hover previews (`?samples=local`); `state` shows the loading or the empty state.
 */
export function LibraryDemo({ state, localSamples = false }: { state?: DemoState; localSamples?: boolean }) {
  const [assets, setAssets] = React.useState<Asset[]>(SAMPLE_ASSETS);
  const [urls, setUrls] = React.useState<string[]>([]);
  const shown = React.useMemo(() => withLocalPreviews(assets, urls), [assets, urls]);

  return (
    <div className="space-y-4">
      {localSamples ? <LocalSamplesPicker onUrls={setUrls} /> : null}
      <LibraryGrid
        assets={state === "empty" ? [] : shown}
        loading={state === "loading"}
        onDelete={(asset) => setAssets((current) => current.filter((item) => item.id !== asset.id))}
        onRetry={(asset) => setAssets((current) => current.map((item) => (item.id === asset.id ? { ...item, status: "processing", progress: undefined } : item)))}
      />
    </div>
  );
}
