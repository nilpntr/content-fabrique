import { ChevronLeft, ChevronRight, MapPin, Pause, Play } from "lucide-react";
import type * as React from "react";
import { Button } from "@/components/ui/button";
import { cn, hue } from "@/lib/utils";
import { pinFromPoint } from "./lib/selection";
import { formatTimecode } from "./lib/time";
import { entryAt } from "./lib/time-map";
import type { OutputFormat, Pin, ReviewVersion } from "./types";
import { type PlayerController, usePlayhead } from "./use-player";

const RATIO: Record<OutputFormat, [number, number]> = { "9:16": [9, 16], "1:1": [1, 1], "4:5": [4, 5], "16:9": [16, 9] };

export interface FramePin {
  pin: Pin;
  /** "draft" is the pin being placed; "selected" belongs to the comment picked in the side panel. */
  tone: "draft" | "selected";
}

export interface PreviewPlayerProps {
  player: PlayerController;
  version: ReviewVersion;
  /** A video to play instead of the generated placeholder. */
  src?: string;
  assetNames?: Record<string, string>;
  pinMode: boolean;
  onPinModeChange: (on: boolean) => void;
  onPlacePin: (pin: Pin) => void;
  pins: FramePin[];
}

export function PreviewPlayer({ player, version, src, assetNames, pinMode, onPinModeChange, onPlacePin, pins }: PreviewPlayerProps) {
  const [w, h] = RATIO[version.output.format];

  const frameClick = (event: React.MouseEvent<HTMLButtonElement>) => {
    if (!pinMode) return player.toggle();
    const rect = event.currentTarget.getBoundingClientRect();
    // A keyboard "click" has no pointer position: pin the middle of the frame.
    onPlacePin(event.detail === 0 ? { x: 0.5, y: 0.5 } : pinFromPoint(event.clientX, event.clientY, rect));
  };

  return (
    <div className="overflow-hidden rounded-xl bg-surface-raised shadow-soft ring-1 ring-border">
      <div
        className="flex items-center justify-center bg-neutral-950 p-3 [--stage-h:min(52svh,34rem)]"
        style={{ minHeight: "calc(var(--stage-h) + 1.5rem)" }}
      >
        <div
          className="relative overflow-hidden rounded-lg bg-black shadow-[0_0_0_1px_rgba(255,255,255,0.08)]"
          style={{ aspectRatio: `${w} / ${h}`, width: `min(100%, calc(var(--stage-h) * ${w} / ${h}))` }}
        >
          {src ? (
            // biome-ignore lint/a11y/useMediaCaption: a work-in-progress preview has no captions track.
            <video
              ref={player.videoRef}
              src={src}
              className="size-full object-contain"
              playsInline
              preload="metadata"
              onLoadedMetadata={(event) => player.syncMedia(event.currentTarget)}
            />
          ) : (
            <PlaceholderFrame version={version} player={player} assetNames={assetNames} />
          )}

          <button
            type="button"
            aria-label={pinMode ? "Place a pin on the frame" : player.playing ? "Pause" : "Play"}
            data-testid="frame-overlay"
            onClick={frameClick}
            className={cn(
              "absolute inset-0 outline-none focus-visible:ring-2 focus-visible:ring-brand focus-visible:ring-inset",
              pinMode ? "cursor-crosshair bg-brand/10 ring-2 ring-brand ring-inset" : "cursor-default",
            )}
          />

          {pins.map(({ pin, tone }) => (
            <span
              key={`${tone}-${pin.x}-${pin.y}`}
              data-pin={tone}
              className={cn(
                "pointer-events-none absolute grid size-6 -translate-x-1/2 -translate-y-1/2 place-items-center rounded-full ring-2 ring-white shadow-[0_2px_8px_rgba(0,0,0,0.5)]",
                tone === "draft" ? "bg-brand" : "bg-white text-neutral-950",
              )}
              style={{ left: `${pin.x * 100}%`, top: `${pin.y * 100}%` }}
            >
              <MapPin className="size-3.5" aria-hidden />
            </span>
          ))}
        </div>
      </div>

      <div className="flex flex-wrap items-center gap-x-2 gap-y-1 border-t border-hairline px-3 py-2">
        <Button
          variant="secondary"
          size="icon"
          aria-label={player.playing ? "Pause" : "Play"}
          title={player.playing ? "Pause (Space or K)" : "Play (Space or K)"}
          onClick={player.toggle}
        >
          {player.playing ? <Pause /> : <Play />}
        </Button>
        <Button variant="ghost" size="icon" aria-label="Previous frame" title="Previous frame (,)" onClick={() => player.step(-1)}>
          <ChevronLeft />
        </Button>
        <Button variant="ghost" size="icon" aria-label="Next frame" title="Next frame (.)" onClick={() => player.step(1)}>
          <ChevronRight />
        </Button>
        <PlayheadControls player={player} />
        <Button
          variant={pinMode ? "default" : "secondary"}
          size="sm"
          aria-pressed={pinMode}
          aria-label="Pin"
          className="ml-auto"
          title="Pin a comment on the frame (P)"
          onClick={() => onPinModeChange(!pinMode)}
        >
          <MapPin />
          <span className="hidden sm:inline">Pin</span>
        </Button>
      </div>
    </div>
  );
}

/** The stand-in for a render: a colour per clip, the clip's name and a mark that moves, so seeking and stepping show. */
function PlaceholderFrame({
  version,
  player,
  assetNames,
}: {
  version: ReviewVersion;
  player: PlayerController;
  assetNames?: Record<string, string>;
}) {
  const currentMs = usePlayhead(player);
  const entry = entryAt(version.time_map, Math.min(currentMs, version.duration_ms - 1));
  const base = hue(entry?.asset_id ?? "none");
  const progress = entry ? (currentMs - entry.out_start_ms) / Math.max(entry.out_end_ms - entry.out_start_ms, 1) : 0;
  const angle = progress * Math.PI * 2;

  return (
    <div
      role="img"
      aria-label={`Placeholder frame at ${formatTimecode(currentMs)}${entry ? `, clip ${entry.item_id}` : ""}`}
      data-testid="placeholder-frame"
      className="relative size-full select-none text-white"
      style={{ background: `linear-gradient(165deg, hsl(${base} 52% 44%), hsl(${(base + 50) % 360} 58% 20%))` }}
    >
      <div
        className="absolute aspect-square w-[24%] rounded-full bg-white/20 ring-1 ring-white/40"
        style={{ left: `${50 + Math.cos(angle) * 26}%`, top: `${38 + Math.sin(angle) * 14}%`, transform: "translate(-50%, -50%)" }}
      />
      <div className="absolute inset-x-0 top-[8%] text-center text-[11px] font-medium tracking-[0.14em] text-white/70 uppercase">
        Placeholder preview
      </div>
      <div className="absolute inset-x-0 bottom-[12%] px-4 text-center">
        <div className="font-mono text-[11px] text-white/70">{entry?.item_id ?? "end"}</div>
        <div className="text-base font-semibold text-balance">{entry ? (assetNames?.[entry.asset_id] ?? entry.asset_id) : "End of video"}</div>
      </div>
    </div>
  );
}

/** The clock and the scrub slider: the parts of the controls that follow the playhead. */
function PlayheadControls({ player }: { player: PlayerController }) {
  const currentMs = usePlayhead(player);
  return (
    <>
      <output className="font-mono text-[12.5px] tabular-nums text-muted-foreground" aria-label="Playhead">
        <span className="text-foreground">{formatTimecode(currentMs)}</span> / {formatTimecode(player.durationMs)}
      </output>
      <input
        type="range"
        aria-label="Scrub"
        min={0}
        max={player.durationMs}
        step={1}
        value={currentMs}
        onChange={(event) => player.seek(Number(event.target.value))}
        className="order-last h-5 min-w-40 basis-full cursor-pointer accent-brand sm:order-none sm:flex-1 sm:basis-0"
      />
    </>
  );
}
