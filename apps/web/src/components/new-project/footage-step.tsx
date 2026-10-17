import { Check, Clock, Search } from "lucide-react";
import * as React from "react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { cn } from "@/lib/utils";
import { Chip } from "./chip";
import { formatDuration } from "./duration";
import type { Asset, AssetId } from "./types";

export function totalDurationMs(assets: readonly Asset[], ids: readonly AssetId[]): number {
  const byId = new Map(assets.map((asset) => [asset.id, asset]));
  return ids.reduce((sum, id) => sum + (byId.get(id)?.durationMs ?? 0), 0);
}

/**
 * Pick clips from the library. Each card is a real checkbox laid over the whole card, so a click, a shift-click
 * (the range from the last clicked clip, among the clips currently shown) and the keyboard all work natively.
 * `selectedIds` keeps the order the clips were picked in.
 */
export function FootageStep({
  assets,
  selectedIds,
  onChange,
}: {
  assets: readonly Asset[];
  selectedIds: readonly AssetId[];
  onChange: (ids: AssetId[]) => void;
}) {
  const [query, setQuery] = React.useState("");
  const [activeTags, setActiveTags] = React.useState<string[]>([]);
  const anchor = React.useRef<AssetId | null>(null);

  const tags = React.useMemo(() => [...new Set(assets.flatMap((asset) => asset.tags))].sort(), [assets]);
  const visible = React.useMemo(() => {
    const needle = query.trim().toLowerCase();
    return assets.filter(
      (asset) =>
        (needle === "" || asset.title.toLowerCase().includes(needle) || asset.tags.some((tag) => tag.toLowerCase().includes(needle))) &&
        (activeTags.length === 0 || asset.tags.some((tag) => activeTags.includes(tag))),
    );
  }, [assets, query, activeTags]);
  const selected = React.useMemo(() => new Set<AssetId>(selectedIds), [selectedIds]);
  const hiddenSelected = selectedIds.filter((id) => !visible.some((asset) => asset.id === id)).length;

  function toggle(id: AssetId, range: boolean) {
    const willSelect = !selected.has(id);
    const from = anchor.current ? visible.findIndex((asset) => asset.id === anchor.current) : -1;
    const to = visible.findIndex((asset) => asset.id === id);
    anchor.current = id;
    if (range && from >= 0 && to >= 0) {
      const ids = visible.slice(Math.min(from, to), Math.max(from, to) + 1).map((asset) => asset.id);
      onChange(willSelect ? [...selectedIds, ...ids.filter((rangeId) => !selected.has(rangeId))] : selectedIds.filter((selectedId) => !ids.includes(selectedId)));
      return;
    }
    onChange(willSelect ? [...selectedIds, id] : selectedIds.filter((selectedId) => selectedId !== id));
  }

  function selectAllVisible() {
    onChange([...selectedIds, ...visible.filter((asset) => !selected.has(asset.id)).map((asset) => asset.id)]);
  }

  function resetFilters() {
    setQuery("");
    setActiveTags([]);
  }

  const count = selectedIds.length;
  const allVisibleSelected = visible.length > 0 && visible.every((asset) => selected.has(asset.id));

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center gap-3">
        <div className="relative w-full sm:max-w-xs">
          <Search aria-hidden="true" className="pointer-events-none absolute top-1/2 left-3 size-4 -translate-y-1/2 text-subtle-foreground" />
          <Input
            type="search"
            aria-label="Search clips"
            placeholder="Search by title or tag"
            value={query}
            onChange={(event) => setQuery(event.target.value)}
            className="pl-9"
          />
        </div>
        <fieldset className="flex min-w-0 flex-wrap gap-1.5">
          <legend className="sr-only">Filter by tag</legend>
          {tags.map((tag) => (
            <Chip
              key={tag}
              pressed={activeTags.includes(tag)}
              onClick={() => setActiveTags((current) => (current.includes(tag) ? current.filter((t) => t !== tag) : [...current, tag]))}
            >
              {tag}
            </Chip>
          ))}
        </fieldset>
      </div>

      <div className="flex flex-wrap items-center justify-between gap-2">
        <p aria-live="polite" className="flex items-center gap-1.5 text-[13.5px] text-muted-foreground">
          <Clock aria-hidden="true" className="size-4 text-subtle-foreground" />
          <span className="font-medium text-foreground tabular-nums">
            {count} {count === 1 ? "clip" : "clips"} selected
          </span>
          <span aria-hidden="true">·</span>
          <span className="tabular-nums">{formatDuration(totalDurationMs(assets, selectedIds))} total</span>
          {hiddenSelected > 0 ? <span className="text-subtle-foreground">({hiddenSelected} hidden by the filter)</span> : null}
        </p>
        <div className="flex gap-1.5">
          <Button variant="ghost" size="sm" onClick={selectAllVisible} disabled={visible.length === 0 || allVisibleSelected}>
            Select all{visible.length !== assets.length ? ` ${visible.length} shown` : ""}
          </Button>
          <Button variant="ghost" size="sm" onClick={() => onChange([])} disabled={count === 0}>
            Clear
          </Button>
        </div>
      </div>

      {visible.length === 0 ? (
        <div className="flex flex-col items-center gap-2 rounded-2xl bg-well py-12 text-center shadow-well">
          <p className="font-medium">No clips match</p>
          <p className="text-muted-foreground">Try another word or remove a tag.</p>
          <Button variant="secondary" size="sm" onClick={resetFilters}>
            Reset filters
          </Button>
        </div>
      ) : (
        <ul className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-4 xl:grid-cols-5">
          {visible.map((asset) => (
            <ClipCard key={asset.id} asset={asset} selected={selected.has(asset.id)} onToggle={toggle} />
          ))}
        </ul>
      )}
    </div>
  );
}

function ClipCard({
  asset,
  selected,
  onToggle,
}: {
  asset: Asset;
  selected: boolean;
  onToggle: (id: AssetId, range: boolean) => void;
}) {
  const titleId = React.useId();
  const metaId = React.useId();
  return (
    <li className="relative">
      <input
        type="checkbox"
        checked={selected}
        aria-labelledby={titleId}
        aria-describedby={metaId}
        onChange={(event) => onToggle(asset.id, (event.nativeEvent as MouseEvent).shiftKey === true)}
        className="peer absolute inset-0 z-10 size-full cursor-pointer appearance-none rounded-xl opacity-0"
      />
      <div
        data-selected={selected}
        className={cn(
          "overflow-hidden rounded-xl bg-surface-raised shadow-soft ring-1 ring-border select-none",
          "transition-[box-shadow,background-color,translate] duration-(--dur-base) ease-out",
          "peer-hover:ring-foreground/25 motion-safe:peer-hover:-translate-y-0.5",
          "peer-focus-visible:ring-2 peer-focus-visible:ring-brand peer-focus-visible:ring-offset-2 peer-focus-visible:ring-offset-background",
          "data-[selected=true]:bg-brand-wash data-[selected=true]:ring-2 data-[selected=true]:ring-brand",
        )}
      >
        <div className="relative aspect-3/4 bg-wash">
          <img src={asset.posterUrl} alt="" loading="lazy" draggable={false} className="size-full object-cover" />
          <span className="absolute right-1.5 bottom-1.5 rounded-md bg-black/65 px-1.5 py-0.5 font-mono text-[11px] leading-4 text-white tabular-nums">
            {formatDuration(asset.durationMs)}
          </span>
          <span
            aria-hidden="true"
            className={cn(
              "absolute top-2 left-2 flex size-5 items-center justify-center rounded-md shadow-raised transition-[background-color,box-shadow] duration-(--dur-fast)",
              selected ? "bg-brand text-primary-foreground" : "bg-surface-raised/85 text-transparent",
            )}
          >
            <Check className="size-3.5" strokeWidth={3} />
          </span>
        </div>
        <div className="space-y-1 p-2.5">
          <p id={titleId} className="truncate text-[13.5px] font-medium">
            {asset.title}
          </p>
          <p id={metaId} className="truncate text-[12.5px] text-muted-foreground">
            <span className="sr-only">{formatDuration(asset.durationMs)}, </span>
            {asset.tags.join(" · ")}
          </p>
        </div>
      </div>
    </li>
  );
}
