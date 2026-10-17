import { ChevronDown, Film } from "lucide-react";
import * as React from "react";
import { Chip } from "@/components/new-project/chip";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { cn } from "@/lib/utils";
import { CARD_RATIO, ClipCard } from "./clip-card";
import type { Asset } from "./types";

export const SORTS = [
  { id: "newest", label: "Newest first" },
  { id: "longest", label: "Longest first" },
  { id: "shortest", label: "Shortest first" },
] as const;
export type SortId = (typeof SORTS)[number]["id"];

export const FILTERS = [
  { id: "all", label: "All" },
  { id: "ready", label: "Ready" },
  { id: "processing", label: "Processing" },
  { id: "failed", label: "Failed" },
] as const;
export type FilterId = (typeof FILTERS)[number]["id"];

/** The filter a clip belongs to. Uploading, queued and processing clips are all "processing" to the creator. */
export function filterOf(asset: Pick<Asset, "status">): Exclude<FilterId, "all"> {
  if (asset.status === "ready" || asset.status === "failed") return asset.status;
  return "processing";
}

export function sortAssets(assets: readonly Asset[], sort: SortId): Asset[] {
  // Compared as instants: ISO strings with different offsets or precision do not sort as text. An unparsable date counts as oldest.
  const time = (asset: Asset) => Date.parse(asset.createdAt) || 0;
  const byNewest = (a: Asset, b: Asset) => time(b) - time(a) || a.id.localeCompare(b.id);
  const compare: Record<SortId, (a: Asset, b: Asset) => number> = {
    newest: byNewest,
    longest: (a, b) => b.durationMs - a.durationMs || byNewest(a, b),
    shortest: (a, b) => a.durationMs - b.durationMs || byNewest(a, b),
  };
  return [...assets].sort(compare[sort]);
}

const GRID = "grid grid-cols-[repeat(auto-fill,minmax(min(100%,13rem),1fr))] gap-4";

export interface LibraryGridProps {
  assets: readonly Asset[];
  loading?: boolean;
  onOpen?: (asset: Asset) => void;
  onDelete?: (asset: Asset) => void;
  onRetry?: (asset: Asset) => void;
  /** Shows an upload button in the empty state. */
  onUpload?: () => void;
  className?: string;
}

export function LibraryGrid({ assets, loading = false, onOpen, onDelete, onRetry, onUpload, className }: LibraryGridProps) {
  const [sort, setSort] = React.useState<SortId>("newest");
  const [filter, setFilter] = React.useState<FilterId>("all");
  const sortId = React.useId();

  const counts = React.useMemo(() => {
    const result: Record<FilterId, number> = { all: assets.length, ready: 0, processing: 0, failed: 0 };
    for (const asset of assets) result[filterOf(asset)]++;
    return result;
  }, [assets]);
  const visible = React.useMemo(
    () => sortAssets(assets, sort).filter((asset) => filter === "all" || filterOf(asset) === filter),
    [assets, sort, filter],
  );

  if (loading) {
    return (
      <div aria-busy="true" className={className}>
        <p role="status" className="sr-only">
          Loading your clips
        </p>
        <ul className={GRID}>
          {Array.from({ length: 8 }, (_, i) => (
            // biome-ignore lint/suspicious/noArrayIndexKey: static skeleton cards, the list never reorders.
            <li key={i} className="animate-rise overflow-hidden rounded-xl bg-surface-raised shadow-soft ring-1 ring-border" style={{ animationDelay: `${i * 40}ms` }}>
              <Skeleton className="w-full rounded-none" style={{ aspectRatio: CARD_RATIO }} />
              <div className="space-y-2 p-2.5">
                <Skeleton className="h-3.5 w-3/4" />
                <Skeleton className="h-5 w-1/2 rounded-full" />
              </div>
            </li>
          ))}
        </ul>
      </div>
    );
  }

  if (assets.length === 0) {
    return (
      <div className={cn("flex flex-col items-center gap-2 rounded-2xl bg-well px-6 py-16 text-center shadow-well", className)}>
        <span aria-hidden="true" className="mb-1 flex size-10 items-center justify-center rounded-xl bg-surface-raised text-muted-foreground shadow-raised">
          <Film className="size-5" />
        </span>
        <h2 className="text-[15px] font-semibold tracking-[-0.01em]">Upload your first clips</h2>
        <p className="max-w-sm text-[13px] text-muted-foreground text-pretty">
          Add raw footage from your phone or camera. Previews are ready within a minute, and you can hover a clip to play it.
        </p>
        {onUpload ? (
          <Button className="mt-2" onClick={onUpload}>
            Upload clips
          </Button>
        ) : null}
      </div>
    );
  }

  return (
    <div className={cn("space-y-4", className)}>
      <div className="flex flex-wrap items-center justify-between gap-3">
        <fieldset className="flex min-w-0 flex-wrap gap-1.5">
          <legend className="sr-only">Filter by status</legend>
          {FILTERS.filter((item) => item.id === "all" || item.id === filter || counts[item.id] > 0).map((item) => (
            <Chip key={item.id} pressed={filter === item.id} onClick={() => setFilter(item.id)}>
              {item.label}
              <span className="text-subtle-foreground tabular-nums">{counts[item.id]}</span>
            </Chip>
          ))}
        </fieldset>
        <div className="flex items-center gap-2">
          <label htmlFor={sortId} className="text-[13px] text-muted-foreground">
            Sort
          </label>
          <div className="relative">
            <select
              id={sortId}
              value={sort}
              onChange={(event) => setSort(event.target.value as SortId)}
              className="h-8.5 cursor-pointer appearance-none rounded-[10px] bg-well py-0 pr-8 pl-3 text-[13.5px] text-foreground shadow-well outline-none hover:shadow-[inset_0_0_0_1px_var(--input)] focus-visible:bg-surface-raised focus-visible:shadow-[inset_0_0_0_1px_var(--brand),0_0_0_3px_var(--brand-wash)]"
            >
              {SORTS.map((item) => (
                <option key={item.id} value={item.id}>
                  {item.label}
                </option>
              ))}
            </select>
            <ChevronDown aria-hidden="true" className="pointer-events-none absolute top-1/2 right-2.5 size-4 -translate-y-1/2 text-subtle-foreground" />
          </div>
        </div>
      </div>

      <p aria-live="polite" className="text-[13px] text-muted-foreground tabular-nums">
        {visible.length} {visible.length === 1 ? "clip" : "clips"}
      </p>

      {visible.length === 0 ? (
        <div className="flex flex-col items-center gap-2 rounded-2xl bg-well py-12 text-center shadow-well">
          <p className="font-medium">No clips match</p>
          <p className="text-muted-foreground">Nothing in the library has this status.</p>
          <Button variant="secondary" size="sm" onClick={() => setFilter("all")}>
            Show all clips
          </Button>
        </div>
      ) : (
        <ul className={GRID}>
          {visible.map((asset) => (
            <li key={asset.id} className="animate-rise">
              <ClipCard asset={asset} onOpen={onOpen} onDelete={onDelete} onRetry={onRetry} />
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
