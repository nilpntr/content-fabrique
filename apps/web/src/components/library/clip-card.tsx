import { Film, MoreHorizontal, RotateCw, Trash2 } from "lucide-react";
import * as React from "react";
import { Button } from "@/components/ui/button";
import { Dialog, DialogClose, DialogContent, DialogDescription, DialogFooter, DialogTitle } from "@/components/ui/dialog";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { cn } from "@/lib/utils";
import { formatDuration } from "@/components/new-project/duration";
import { tileBackground } from "./scrub";
import type { Asset, AssetStatus } from "./types";
import { useClipPreview } from "./use-clip-preview";
import { useSpriteModel } from "./use-sprite-model";

/** The card's picture area is always this ratio; a clip of another shape is letterboxed inside it. */
export const CARD_RATIO = 4 / 3;

const STATUS_LABEL: Record<AssetStatus, string> = {
  uploading: "Uploading",
  uploaded: "Queued",
  processing: "Processing",
  ready: "Ready",
  failed: "Failed",
};

const BUSY: ReadonlySet<AssetStatus> = new Set(["uploading", "uploaded", "processing"]);

export function statusText(asset: Pick<Asset, "status" | "progress">): string {
  const label = STATUS_LABEL[asset.status];
  return (asset.status === "uploading" || asset.status === "processing") && asset.progress !== undefined
    ? `${label} ${Math.round(clampPercent(asset.progress))}%`
    : label;
}

function clampPercent(value: number): number {
  return Math.min(100, Math.max(0, value));
}

export function StatusChip({ asset }: { asset: Pick<Asset, "status" | "progress"> }) {
  const { status } = asset;
  return (
    <span
      data-slot="status-chip"
      data-status={status}
      className={cn(
        "inline-flex h-5 shrink-0 items-center gap-1.5 rounded-full bg-wash px-2 text-[11.5px] font-medium text-muted-foreground tabular-nums",
        status === "failed" && "bg-del-wash text-destructive",
        status === "ready" && "bg-add-wash text-foreground",
      )}
    >
      <span
        aria-hidden="true"
        className={cn(
          "size-1.5 rounded-full",
          status === "ready" && "bg-good",
          status === "failed" && "bg-destructive",
          status === "uploading" && "bg-brand motion-safe:animate-pulse",
          status === "processing" && "bg-brand motion-safe:animate-pulse",
          status === "uploaded" && "bg-subtle-foreground",
        )}
      />
      {statusText(asset)}
    </span>
  );
}

/** A bar over the bottom of the picture: filled when the progress is known, a pulsing full bar when it is not. */
function ProgressBar({ asset }: { asset: Asset }) {
  const known = asset.progress !== undefined && asset.status !== "uploaded";
  const value = known ? clampPercent(asset.progress ?? 0) : undefined;
  return (
    <div
      role="progressbar"
      aria-label={`${STATUS_LABEL[asset.status]} ${asset.title}`}
      aria-valuemin={0}
      aria-valuemax={100}
      aria-valuenow={value === undefined ? undefined : Math.round(value)}
      data-indeterminate={value === undefined}
      className="absolute inset-x-0 bottom-0 h-1 bg-white/25"
    >
      <div
        className={cn("h-full bg-brand transition-[width] duration-(--dur-slow) ease-out", value === undefined && "w-full motion-safe:animate-pulse")}
        style={value === undefined ? undefined : { width: `${value}%` }}
      />
    </div>
  );
}

function HoverVideo({ src, hidden, onFail }: { src: string; hidden: boolean; onFail: () => void }) {
  const ref = React.useRef<HTMLVideoElement>(null);
  const [playing, setPlaying] = React.useState(false);

  React.useEffect(() => {
    const video = ref.current;
    if (!video) return;
    if (hidden) {
      video.pause?.();
      return;
    }
    // play() rejects when the browser blocks it or the source is gone; the poster stays, which is fine.
    Promise.resolve(video.play?.()).catch(() => {});
  }, [hidden]);

  // Unmounting alone does not stop the download: drop the source so the browser aborts the request.
  React.useEffect(() => {
    const video = ref.current;
    return () => {
      if (!video) return;
      video.removeAttribute("src");
      video.load?.();
    };
  }, []);

  return (
    <video
      ref={ref}
      src={src}
      muted
      loop
      playsInline
      preload="none"
      tabIndex={-1}
      aria-hidden="true"
      data-slot="hover-video"
      onPlaying={() => setPlaying(true)}
      onError={onFail}
      className={cn("absolute inset-0 size-full object-cover", playing && !hidden ? "opacity-100" : "opacity-0")}
    />
  );
}

export interface ClipCardProps {
  asset: Asset;
  /** The card (the picture or the title) was activated: a click, Enter or a tap. Not a long press. */
  onOpen?: (asset: Asset) => void;
  /** Called after the creator confirmed the delete dialog. */
  onDelete?: (asset: Asset) => void;
  /** Called by the Retry button of a failed clip. */
  onRetry?: (asset: Asset) => void;
  className?: string;
}

export function ClipCard({ asset, onOpen, onDelete, onRetry, className }: ClipCardProps) {
  const ready = asset.status === "ready";
  const busy = BUSY.has(asset.status);
  const title = asset.title.trim() || "Untitled clip";
  const [confirming, setConfirming] = React.useState(false);
  // The URL that failed, so a new preview URL (a reprocessed clip) gets its chance again.
  const [failedUrl, setFailedUrl] = React.useState<string | undefined>();

  const preview = useClipPreview({
    id: asset.id,
    enabled: ready,
    hasVideo: Boolean(asset.hoverPreviewUrl),
    hasSprite: Boolean(asset.sprite),
  });
  const sprite = useSpriteModel(asset.sprite, asset.durationMs, ready && preview.hovering);
  const frame = sprite?.at(preview.scrub ?? 0);
  // Without a frame to show (no sprite, or its track has not loaded) the video is left alone.
  const scrubbing = preview.scrub !== null && frame !== undefined;
  const showVideo = preview.showVideo && asset.hoverPreviewUrl !== undefined && failedUrl !== asset.hoverPreviewUrl;

  const portrait = asset.width / asset.height <= CARD_RATIO;
  const tags = asset.tags.slice(0, 1);
  const moreTags = asset.tags.length - tags.length;

  return (
    <div data-slot="clip-card-root" className={cn("group/clip relative", className)}>
      <article
        data-slot="clip-card"
        data-status={asset.status}
        className="overflow-hidden rounded-xl bg-surface-raised shadow-soft ring-1 ring-border transition-[box-shadow] duration-(--dur-base) ease-out group-hover/clip:ring-foreground/25 group-focus-within/clip:ring-foreground/25"
      >
        <div
          data-slot="clip-media"
          data-previewing={showVideo}
          data-scrubbing={scrubbing}
          {...preview.handlers}
          className="relative touch-pan-y bg-[oklch(0.17_0.006_80)] select-none [-webkit-touch-callout:none]"
          style={{ aspectRatio: CARD_RATIO }}
        >
          <button
            type="button"
            aria-label={`Open ${title}`}
            onClick={() => {
              if (preview.consumeClick()) return;
              onOpen?.(asset);
            }}
            className="absolute inset-0 flex cursor-pointer items-center justify-center outline-none focus-visible:ring-2 focus-visible:ring-brand focus-visible:ring-inset"
          >
            <span
              data-slot="clip-picture"
              className={cn("relative block overflow-hidden", portrait ? "h-full w-auto max-w-full" : "h-auto w-full max-h-full")}
              style={{ aspectRatio: `${asset.width} / ${asset.height}` }}
            >
              {asset.posterUrl ? (
                <img src={asset.posterUrl} alt="" loading="lazy" draggable={false} className="size-full object-cover" />
              ) : (
                <span className="flex size-full items-center justify-center text-white/40">
                  <Film aria-hidden="true" className="size-7" />
                </span>
              )}
              {sprite && frame && preview.hovering ? (
                <span
                  data-slot="sprite-frame"
                  aria-hidden="true"
                  className={cn("absolute inset-0 bg-no-repeat", scrubbing ? "visible" : "invisible")}
                  style={{ backgroundImage: `url(${JSON.stringify(sprite.url)})`, ...tileBackground(frame.tile, frame.sheet) }}
                />
              ) : null}
              {showVideo && asset.hoverPreviewUrl ? (
                <HoverVideo src={asset.hoverPreviewUrl} hidden={scrubbing} onFail={() => setFailedUrl(asset.hoverPreviewUrl)} />
              ) : null}
            </span>
          </button>

          {busy ? (
            <>
              <span aria-hidden="true" className="pointer-events-none absolute inset-0 bg-black/30" />
              <ProgressBar asset={asset} />
            </>
          ) : null}

          {scrubbing ? (
            <div aria-hidden="true" data-slot="scrub-line" className="pointer-events-none absolute inset-x-0 bottom-0 h-[3px] bg-white/30">
              <div className="h-full bg-brand" style={{ width: `${(preview.scrub ?? 0) * 100}%` }} />
            </div>
          ) : null}

          <DropdownMenu onOpenChange={(open) => open && preview.stop()}>
            <DropdownMenuTrigger
              render={
                <button
                  type="button"
                  aria-label={`Actions for ${title}`}
                  data-slot="clip-menu"
                  className="absolute top-2 right-2 z-10 flex size-7 cursor-pointer items-center justify-center rounded-[8px] bg-black/55 text-white opacity-0 outline-none backdrop-blur-sm transition-opacity duration-(--dur-fast) group-hover/clip:opacity-100 hover:bg-black/70 focus-visible:opacity-100 focus-visible:ring-2 focus-visible:ring-brand data-popup-open:opacity-100 pointer-coarse:opacity-100"
                />
              }
            >
              <MoreHorizontal aria-hidden="true" className="size-4" />
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end">
              <DropdownMenuItem onClick={() => onOpen?.(asset)}>Open</DropdownMenuItem>
              <DropdownMenuSeparator />
              <DropdownMenuItem variant="destructive" onClick={() => setConfirming(true)}>
                <Trash2 aria-hidden="true" />
                Delete
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>

          {ready && asset.durationMs > 0 ? (
            <span className="pointer-events-none absolute right-1.5 bottom-2 rounded bg-black/70 px-1.5 font-mono text-[11px] leading-5 text-white tabular-nums">
              <span className="sr-only">Duration </span>
              {formatDuration(asset.durationMs)}
            </span>
          ) : null}
        </div>

        <div className="space-y-1.5 p-2.5 pr-3">
          <h3 className="truncate text-[13.5px] font-medium tracking-[-0.005em]" title={title}>
            {title}
          </h3>
          <div className="flex min-w-0 items-center gap-1.5">
            <StatusChip asset={asset} />
            {ready
              ? tags.map((tag) => (
                  <span key={tag} className="truncate rounded-full bg-wash px-2 text-[11.5px] leading-5 text-muted-foreground">
                    {tag}
                  </span>
                ))
              : null}
            {ready && moreTags > 0 ? <span className="shrink-0 text-[11.5px] leading-5 text-subtle-foreground tabular-nums" title={asset.tags.join(", ")}>+{moreTags}</span> : null}
          </div>
          {asset.status === "failed" && onRetry && asset.retryable !== false ? (
            <Button variant="secondary" size="sm" onClick={() => onRetry(asset)} aria-label={`Retry ${title}`}>
              <RotateCw aria-hidden="true" />
              Retry
            </Button>
          ) : null}
        </div>
      </article>

      <Dialog open={confirming} onOpenChange={setConfirming}>
        <DialogContent>
          <DialogTitle>Delete this clip?</DialogTitle>
          <DialogDescription>
            “{title}” is removed from your library for good. Projects that use it lose the clip.
          </DialogDescription>
          <DialogFooter>
            <DialogClose render={<Button variant="secondary" />}>Cancel</DialogClose>
            <Button
              className="bg-destructive bg-none text-white shadow-none after:bg-black"
              onClick={() => {
                setConfirming(false);
                onDelete?.(asset);
              }}
            >
              Delete clip
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}
