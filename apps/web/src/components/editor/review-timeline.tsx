import { MessageSquarePlus, X, ZoomIn, ZoomOut } from "lucide-react";
import * as React from "react";
import { Button } from "@/components/ui/button";
import { useHotkey } from "@/hooks/use-hotkey";
import { cn, hue } from "@/lib/utils";
import { CommentComposer } from "./comment-composer";
import { assignLanes, clampZoom, createScale, MAX_ZOOM, MIN_ZOOM, msToPx, pxToMs, rulerStepMs, rulerTicks, scaleWidthPx, type TimelineScale } from "./lib/scale";
import { type Handle, moveHandle, resolveGesture, snapThresholdMs } from "./lib/selection";
import { clamp, formatRange, formatRulerLabel, formatTimecode, stepFrames } from "./lib/time";
import { cutTimes, itemOutputDurationMs } from "./lib/time-map";
import { BeatTicks, ClipBadges, ClipHandle, DownbeatLines, type Focus, type FocusControl, sameFocus, SfxLane, sfxLaneHeight, sfxRows, TimelineDetails, TransitionMarks } from "./timeline-details";
import type { BeatMarker, CommentDraft, MsRange, ReviewVersion } from "./types";
import { type PlayerController, usePlayhead } from "./use-player";

const RULER_H = 26;
/** The clips: a label row and a row of effect badges. */
const FILM_H = 56;
const LANE_H = 20;
const LANE_GAP = 4;
/** Used until the first measurement, and where the layout has no size (a test). */
const FALLBACK_VIEWPORT_PX = 800;
const COMPOSER_W = 320;
const EMPTY_SFX: never[] = [];
const EMPTY_MARKERS: BeatMarker[] = [];

/** A comment drawn on the timeline. Replies are not drawn: they follow the comment they answer. */
export interface TimelineRegion {
  id: string;
  start_ms: number;
  end_ms: number;
  body: string;
  /** `open` is brand, `addressed` (applied in a known version) is green, `neutral` is resolved or applied. */
  tone: "open" | "addressed" | "neutral";
  pinned: boolean;
}

export interface ReviewTimelineProps {
  version: ReviewVersion;
  player: PlayerController;
  assetNames?: Record<string, string>;
  regions: TimelineRegion[];
  selectedRegionId: string | null;
  hoveredRegionId: string | null;
  draft: CommentDraft | null;
  composing: boolean;
  /** The comment text being typed, kept by the screen so a new selection can't take it along. */
  body: string;
  onBodyChange: (body: string) => void;
  /** Shown in the comment box, e.g. why a new selection was ignored. */
  notice?: { text: string };
  /** `compose` opens the comment box (a drag or a click does, the I and O keys don't); `seekTo` moves the playhead if the selection is accepted. */
  onDraftChange: (draft: CommentDraft | null, options?: { compose?: boolean; seekTo?: number }) => void;
  onSaveDraft: (body: string) => void;
  onCancelDraft: () => void;
  onSelectRegion: (id: string) => void;
  onHoverRegion: (id: string | null) => void;
}

function ReviewTimelineView({
  version,
  player,
  assetNames,
  regions,
  selectedRegionId,
  hoveredRegionId,
  draft,
  composing,
  body,
  onBodyChange,
  notice,
  onDraftChange,
  onSaveDraft,
  onCancelDraft,
  onSelectRegion,
  onHoverRegion,
}: ReviewTimelineProps) {
  const viewportRef = React.useRef<HTMLDivElement>(null);
  const contentRef = React.useRef<HTMLDivElement>(null);
  const [measuredPx, setMeasuredPx] = React.useState(0);
  const [scrollLeft, setScrollLeft] = React.useState(0);
  const [zoom, setZoom] = React.useState(MIN_ZOOM);
  /** The range under the pointer while a drag is in progress. */
  const [live, setLive] = React.useState<MsRange | null>(null);
  const press = React.useRef<{ startPx: number; cancelled: boolean } | null>(null);
  const handleDrag = React.useRef<Handle | null>(null);
  const scrubbing = React.useRef(false);
  const keepCenterMs = React.useRef<number | null>(null);

  const viewportPx = measuredPx > 0 ? measuredPx : FALLBACK_VIEWPORT_PX;
  const scale = React.useMemo(() => createScale(version.duration_ms, viewportPx, zoom), [version.duration_ms, viewportPx, zoom]);
  const contentPx = scaleWidthPx(scale);
  const cuts = React.useMemo(() => cutTimes(version.items), [version.items]);
  const snapThreshold = snapThresholdMs(scale);
  const lanes = React.useMemo(() => assignLanes(regions, scale), [regions, scale]);
  const laneCount = Math.max(1, ...[...lanes.values()].map((lane) => lane + 1));
  const lanesH = laneCount * LANE_H + LANE_GAP * 2;

  // What the editor put in the cut: sound effects get a lane of their own, the beat grid goes in the ruler.
  const sfx = version.sfx ?? EMPTY_SFX;
  const markers = version.markers ?? EMPTY_MARKERS;
  const sfxLayout = React.useMemo(() => sfxRows(sfx, scale), [sfx, scale]);
  const sfxH = sfx.length > 0 ? sfxLaneHeight(sfxLayout.count) : 0;
  const [pinnedFocus, setPinnedFocus] = React.useState<Focus | null>(null);
  const [peekFocus, setPeekFocus] = React.useState<Focus | null>(null);
  // Another version has other clips: what was picked is not there any more.
  // biome-ignore lint/correctness/useExhaustiveDependencies: resets when the version on screen changes.
  React.useEffect(() => {
    setPinnedFocus(null);
    setPeekFocus(null);
  }, [version.version]);
  const focusControl = React.useMemo<FocusControl>(
    () => ({
      selected: pinnedFocus,
      onPeek: setPeekFocus,
      onSelect: (focus) => setPinnedFocus((current) => (sameFocus(current, focus) ? null : focus)),
    }),
    [pinnedFocus],
  );

  React.useLayoutEffect(() => {
    const el = viewportRef.current;
    if (!el) return;
    const measure = () => setMeasuredPx(el.getBoundingClientRect().width);
    measure();
    if (typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(measure);
    observer.observe(el);
    return () => observer.disconnect();
  }, []);

  // After a zoom, keep the middle of the view where it was.
  React.useLayoutEffect(() => {
    const el = viewportRef.current;
    if (!el || keepCenterMs.current === null) return;
    el.scrollLeft = Math.max(msToPx(keepCenterMs.current, scale) - el.clientWidth / 2, 0);
    keepCenterMs.current = null;
    setScrollLeft(el.scrollLeft);
  }, [scale]);

  // Zoomed in, follow the playhead during playback and seeks when it leaves the view. It listens to the playhead
  // store directly, so a zoom (which moves no time) never triggers it and cannot undo the keep-centre above.
  React.useEffect(() => {
    if (zoom <= MIN_ZOOM) return;
    return player.playhead.subscribe(() => {
      const el = viewportRef.current;
      if (!el) return;
      const x = msToPx(player.playhead.get(), scale);
      if (x < el.scrollLeft || x > el.scrollLeft + el.clientWidth) el.scrollLeft = Math.max(x - el.clientWidth / 2, 0);
    });
  }, [player.playhead, scale, zoom]);

  const changeZoom = (factor: number) => {
    const el = viewportRef.current;
    const next = clampZoom(zoom * factor);
    if (next === zoom) return;
    if (el) keepCenterMs.current = pxToMs(el.scrollLeft + el.clientWidth / 2, scale);
    setZoom(next);
  };

  /** Pixels from the left edge of the (scrolled) timeline. */
  const pxOf = (event: { clientX: number }) => event.clientX - (contentRef.current?.getBoundingClientRect().left ?? 0);

  // Escape ends a drag in progress without creating anything.
  useHotkey("Escape", () => {
    if (press.current) press.current.cancelled = true;
    setLive(null);
  });

  // --- Drag across the tracks to select a range; a click is a point comment.
  const gestureAt = (event: { clientX: number; altKey: boolean }) =>
    resolveGesture({ startPx: press.current?.startPx ?? 0, endPx: pxOf(event), scale, snapPoints: cuts, snap: !event.altKey });

  const onSurfaceDown = (event: React.PointerEvent<HTMLDivElement>) => {
    if (event.button !== 0) return;
    event.currentTarget.setPointerCapture?.(event.pointerId);
    press.current = { startPx: pxOf(event), cancelled: false };
  };

  const onSurfaceMove = (event: React.PointerEvent<HTMLDivElement>) => {
    if (!press.current || press.current.cancelled) return;
    const gesture = gestureAt(event);
    setLive(gesture.kind === "range" ? { start_ms: gesture.start_ms, end_ms: gesture.end_ms } : null);
  };

  const onSurfaceUp = (event: React.PointerEvent<HTMLDivElement>) => {
    const current = press.current;
    if (!current) return;
    const gesture = gestureAt(event);
    press.current = null;
    setLive(null);
    if (current.cancelled) return;
    const range =
      gesture.kind === "range" ? { start_ms: gesture.start_ms, end_ms: gesture.end_ms } : { start_ms: gesture.ms, end_ms: gesture.ms };
    onDraftChange(range, { compose: true, seekTo: range.start_ms });
  };

  const onSurfaceCancel = () => {
    press.current = null;
    setLive(null);
  };

  // --- Scrubbing on the ruler.
  const scrubTo = (event: { clientX: number }) => player.seek(pxToMs(pxOf(event), scale));

  // --- The ends of the selection.
  const moveEdge = (edge: Handle, ms: number, snap: boolean) => {
    if (!draft || draft.end_ms <= draft.start_ms) return;
    const range = moveHandle(draft, edge, ms, { durationMs: version.duration_ms, snapPoints: cuts, thresholdMs: snap ? snapThreshold : 0 });
    onDraftChange({ ...draft, ...range });
  };

  const shown: CommentDraft | null = live ?? draft;
  const isRange = shown !== null && shown.end_ms > shown.start_ms;

  const composerLeft = draft
    ? clamp(msToPx((draft.start_ms + draft.end_ms) / 2, scale) - scrollLeft + 12 - COMPOSER_W / 2, 0, Math.max(viewportPx + 24 - COMPOSER_W, 0))
    : 0;

  return (
    <section aria-label="Timeline" className="relative rounded-xl bg-surface-raised shadow-soft ring-1 ring-border">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1.5 px-3 pt-2.5 pb-2">
        <h2 className="text-[13px] font-semibold tracking-[-0.01em]">Timeline</h2>

        <output aria-label="Selection" className="flex min-h-7 items-center gap-2 font-mono text-[12px] tabular-nums text-muted-foreground">
          {draft ? (
            draft.end_ms > draft.start_ms ? (
              <>
                <span>
                  In <b className="font-medium text-foreground">{formatTimecode(draft.start_ms)}</b>
                </span>
                <span>
                  Out <b className="font-medium text-foreground">{formatTimecode(draft.end_ms)}</b>
                </span>
                <span>{((draft.end_ms - draft.start_ms) / 1000).toFixed(1)} s</span>
              </>
            ) : (
              <span>
                At <b className="font-medium text-foreground">{formatTimecode(draft.start_ms)}</b>
              </span>
            )
          ) : (
            <span className="hidden font-sans text-[12px] sm:inline">Drag to select a range, click for a point · I and O set in and out</span>
          )}
        </output>

        {draft && (
          <div className="flex items-center gap-1">
            {!composing && (
              <Button variant="secondary" size="sm" title="Comment on the selection (C)" onClick={() => onDraftChange(draft, { compose: true })}>
                <MessageSquarePlus />
                Comment
              </Button>
            )}
            <Button variant="ghost" size="sm" title="Clear the selection (Esc)" onClick={onCancelDraft}>
              <X />
              Clear
            </Button>
          </div>
        )}

        <div className="ml-auto flex items-center gap-0.5">
          <Button variant="ghost" size="icon-sm" aria-label="Zoom out" disabled={zoom <= MIN_ZOOM} onClick={() => changeZoom(0.5)}>
            <ZoomOut />
          </Button>
          <span className="w-9 text-center font-mono text-[12px] tabular-nums text-muted-foreground">{zoom}×</span>
          <Button variant="ghost" size="icon-sm" aria-label="Zoom in" disabled={zoom >= MAX_ZOOM} onClick={() => changeZoom(2)}>
            <ZoomIn />
          </Button>
        </div>
      </div>

      <div className="px-3 pb-3">
        <div
          ref={viewportRef}
          onScroll={(event) => setScrollLeft(event.currentTarget.scrollLeft)}
          className="overflow-x-auto overflow-y-hidden rounded-lg bg-well shadow-well [scrollbar-width:thin]"
        >
          <div ref={contentRef} className="relative" style={{ width: contentPx }}>
            {/* Ruler: scrub here. */}
            <div
              className="relative cursor-ew-resize touch-none border-b border-hairline select-none"
              style={{ height: RULER_H }}
              data-testid="timeline-ruler"
              onPointerDown={(event) => {
                if (event.button !== 0) return;
                event.currentTarget.setPointerCapture?.(event.pointerId);
                scrubbing.current = true;
                scrubTo(event);
              }}
              onPointerMove={(event) => scrubbing.current && scrubTo(event)}
              onPointerUp={() => {
                scrubbing.current = false;
              }}
              onPointerCancel={() => {
                scrubbing.current = false;
              }}
            >
              <RulerMarks scale={scale} cuts={cuts} />
              <BeatTicks markers={markers} scale={scale} />
            </div>

            {/* Filmstrip and comment lane: drag to select. */}
            {/* biome-ignore lint/a11y/useSemanticElements: a drag surface has no native element; the selection has keyboard controls. */}
            <div
              role="group"
              aria-label="Timeline tracks, drag to select a range"
              data-testid="timeline-surface"
              className="relative cursor-crosshair touch-none select-none"
              style={{ height: FILM_H + sfxH + lanesH }}
              onPointerDown={onSurfaceDown}
              onPointerMove={onSurfaceMove}
              onPointerUp={onSurfaceUp}
              onPointerCancel={onSurfaceCancel}
            >
              <Filmstrip version={version} scale={scale} assetNames={assetNames} control={focusControl} />

              <DownbeatLines markers={markers} scale={scale} height={FILM_H + sfxH} />
              <CutLines scale={scale} cuts={cuts} />
              <TransitionMarks items={version.items} scale={scale} height={FILM_H} control={focusControl} />

              {sfxH > 0 ? (
                // biome-ignore lint/a11y/useSemanticElements: a lane drawn on the timeline surface; a fieldset cannot be positioned like this.
                <div aria-label="Sound effects" role="group" className="absolute inset-x-0 border-t border-hairline" style={{ top: FILM_H, height: sfxH }}>
                  <SfxLane sfx={sfx} scale={scale} rows={sfxLayout.rows} control={focusControl} />
                </div>
              ) : null}

              <div className="absolute inset-x-0 border-t border-hairline" style={{ top: FILM_H + sfxH, height: lanesH }}>
                {regions.map((region) => (
                  <RegionMark
                    key={region.id}
                    region={region}
                    scale={scale}
                    lane={lanes.get(region.id) ?? 0}
                    selected={region.id === selectedRegionId}
                    hovered={region.id === hoveredRegionId}
                    onSelect={onSelectRegion}
                    onHover={onHoverRegion}
                  />
                ))}
              </div>

              {shown && (
                <div
                  data-testid="selection"
                  data-live={live ? "true" : undefined}
                  className={cn(
                    "pointer-events-none absolute inset-y-0 z-10",
                    isRange ? "border-x-2 border-brand bg-brand/20" : "w-0.5 -translate-x-1/2 bg-brand",
                  )}
                  style={{
                    left: msToPx(shown.start_ms, scale),
                    width: isRange ? Math.max(msToPx(shown.end_ms - shown.start_ms, scale), 2) : undefined,
                  }}
                >
                  {isRange && !live && (
                    <>
                      <EdgeHandle edge="start" shown={shown} fps={player.fps} duration={version.duration_ms} drag={handleDrag} pxOf={pxOf} scale={scale} onMove={moveEdge} />
                      <EdgeHandle edge="end" shown={shown} fps={player.fps} duration={version.duration_ms} drag={handleDrag} pxOf={pxOf} scale={scale} onMove={moveEdge} />
                    </>
                  )}
                </div>
              )}
            </div>

            <PlayheadMarker player={player} scale={scale} />
          </div>
        </div>

        <div className="mt-2">
          <TimelineDetails
            focus={peekFocus ?? pinnedFocus}
            pinnedFocus={pinnedFocus}
            items={version.items}
            sfx={sfx}
            assetNames={assetNames}
            onClose={() => setPinnedFocus(null)}
          />
        </div>
      </div>

      {draft && composing && (
        <CommentComposer
          draft={draft}
          body={body}
          onBodyChange={onBodyChange}
          notice={notice}
          onSave={onSaveDraft}
          onCancel={onCancelDraft}
          className="absolute top-full z-30 mt-2"
          style={{ left: composerLeft }}
        />
      )}
    </section>
  );
}

const Filmstrip = React.memo(function Filmstrip({
  version,
  scale,
  assetNames,
  control,
}: {
  version: ReviewVersion;
  scale: TimelineScale;
  assetNames?: Record<string, string>;
  control: FocusControl;
}) {
  return (
    <div className="absolute inset-x-0 top-0 overflow-hidden" style={{ height: FILM_H }}>
      {version.items.map((item) => {
        const base = hue(item.asset_id);
        // A clip that starts inside a blend keeps its label clear of the transition's mark.
        const lead = item.transition_in && item.transition_in.type !== "cut" ? Math.max(msToPx(item.transition_in.duration_ms, scale), 24) : 0;
        return (
          <div
            key={item.id}
            data-item={item.id}
            className="absolute top-1 bottom-1 overflow-hidden rounded-[6px] text-white shadow-[inset_0_0_0_1px_rgba(255,255,255,0.18)]"
            style={{
              left: msToPx(item.at_ms, scale),
              width: Math.max(msToPx(itemOutputDurationMs(item), scale) - 1, 2),
              backgroundImage: [
                // The frame boundaries of the placeholder thumbnails.
                "repeating-linear-gradient(90deg, transparent 0 31px, rgba(0,0,0,0.28) 31px 32px)",
                `linear-gradient(100deg, hsl(${base} 48% 46%), hsl(${(base + 40) % 360} 52% 30%))`,
              ].join(", "),
            }}
          >
            <div className="absolute inset-0 flex flex-col justify-between px-1.5 py-1" style={{ paddingLeft: 6 + lead }}>
              <div className="flex min-w-0 items-center gap-1 text-[10.5px] leading-none font-medium drop-shadow-[0_1px_1px_rgba(0,0,0,0.5)]">
                <ClipHandle item={item} control={control} />
                <span className="min-w-0 truncate">
                  {assetNames?.[item.asset_id] ?? item.asset_id}
                  {item.speed_ramp ? (
                    <span className="ml-1 font-mono opacity-80">ramp</span>
                  ) : (
                    item.speed !== 1 && <span className="ml-1 font-mono opacity-80">{item.speed}×</span>
                  )}
                </span>
              </div>
              <ClipBadges item={item} control={control} />
            </div>
          </div>
        );
      })}
    </div>
  );
});

const RegionMark = React.memo(function RegionMark({
  region,
  scale,
  lane,
  selected,
  hovered,
  onSelect,
  onHover,
}: {
  region: TimelineRegion;
  scale: TimelineScale;
  lane: number;
  selected: boolean;
  hovered: boolean;
  onSelect: (id: string) => void;
  onHover: (id: string | null) => void;
}) {
  const isPoint = region.end_ms <= region.start_ms;
  const left = msToPx(region.start_ms, scale);
  const label = `${isPoint ? "Point" : "Range"} comment ${formatRange(region.start_ms, region.end_ms)}: ${region.body}`;

  return (
    <button
      type="button"
      aria-label={label}
      title={label}
      data-region-id={region.id}
      data-tone={region.tone}
      data-selected={selected}
      data-hovered={hovered}
      onPointerDown={(event) => event.stopPropagation()}
      onClick={() => onSelect(region.id)}
      onPointerEnter={() => onHover(region.id)}
      onPointerLeave={() => onHover(null)}
      onFocus={() => onHover(region.id)}
      onBlur={() => onHover(null)}
      className={cn(
        "absolute cursor-pointer overflow-hidden border text-left text-[10.5px] leading-[1.15rem] font-medium text-foreground outline-none transition-[background-color,box-shadow] focus-visible:ring-2 focus-visible:ring-brand",
        isPoint ? "size-3.5 -translate-x-1/2 rounded-full" : "rounded-md px-1.5 whitespace-nowrap",
        "data-[tone=open]:border-brand/60 data-[tone=open]:bg-brand/20 data-[tone=addressed]:border-good/60 data-[tone=addressed]:bg-good/20 data-[tone=neutral]:border-border data-[tone=neutral]:bg-muted-foreground/15",
        "data-[hovered=true]:data-[tone=open]:bg-brand/35 data-[hovered=true]:data-[tone=addressed]:bg-good/35 data-[hovered=true]:data-[tone=neutral]:bg-muted-foreground/25",
        "data-[selected=true]:z-10 data-[selected=true]:ring-2 data-[selected=true]:ring-foreground data-[selected=true]:data-[tone=open]:bg-brand/50 data-[selected=true]:data-[tone=addressed]:bg-good/50 data-[selected=true]:data-[tone=neutral]:bg-muted-foreground/40",
      )}
      style={{
        left,
        top: LANE_GAP + lane * LANE_H,
        height: LANE_H - 4,
        width: isPoint ? undefined : Math.max(msToPx(region.end_ms - region.start_ms, scale), 14),
      }}
    >
      {!isPoint && (
        <>
          {region.pinned && <span aria-hidden className="mr-1 inline-block size-1.5 rounded-full bg-foreground align-middle" />}
          {region.body}
        </>
      )}
    </button>
  );
});

/** The ruler's ticks, labels and cut diamonds. Static between zooms. */
const RulerMarks = React.memo(function RulerMarks({ scale, cuts }: { scale: TimelineScale; cuts: number[] }) {
  const ticks = React.useMemo(() => rulerTicks(scale), [scale]);
  const step = rulerStepMs(scale);
  return (
    <>
      {ticks.map((tick) => (
        <div key={tick.ms} className="pointer-events-none absolute bottom-0" style={{ left: msToPx(tick.ms, scale), height: tick.major ? 10 : 5 }}>
          <div className={cn("h-full w-px", tick.major ? "bg-muted-foreground/70" : "bg-muted-foreground/30")} />
          {tick.major && (
            <span className="absolute bottom-3 left-1 font-mono text-[10.5px] leading-none whitespace-nowrap text-muted-foreground tabular-nums">
              {formatRulerLabel(tick.ms, step)}
            </span>
          )}
        </div>
      ))}
      {cuts.map((ms) => (
        <div
          key={ms}
          aria-hidden
          className="pointer-events-none absolute bottom-0.5 size-1.5 -translate-x-1/2 rotate-45 bg-brand"
          style={{ left: msToPx(ms, scale) }}
        />
      ))}
    </>
  );
});

/** The white cut lines across the filmstrip. */
const CutLines = React.memo(function CutLines({ scale, cuts }: { scale: TimelineScale; cuts: number[] }) {
  return (
    <>
      {cuts.map((ms) => (
        <div
          key={ms}
          aria-hidden
          data-cut={ms}
          className="pointer-events-none absolute top-0 w-0.5 -translate-x-1/2 bg-white shadow-[0_0_0_1px_rgba(0,0,0,0.35)]"
          style={{ left: msToPx(ms, scale), height: FILM_H }}
        />
      ))}
    </>
  );
});

/** The playhead line: the one part of the timeline that renders on every frame of playback. */
function PlayheadMarker({ player, scale }: { player: PlayerController; scale: TimelineScale }) {
  const currentMs = usePlayhead(player);
  return (
    <div
      aria-hidden
      data-testid="playhead"
      className="pointer-events-none absolute inset-y-0 z-20 w-0.5 -translate-x-1/2 bg-foreground"
      style={{ left: msToPx(currentMs, scale) }}
    >
      <div className="absolute top-0 left-1/2 h-0 w-0 -translate-x-1/2 border-x-[5px] border-t-[7px] border-x-transparent border-t-foreground" />
    </div>
  );
}

export const ReviewTimeline = React.memo(ReviewTimelineView);

/** One end of the selected range: drag it, or use the arrow keys (Shift for ten frames). */
function EdgeHandle({
  edge,
  shown,
  fps,
  duration,
  drag,
  pxOf,
  scale,
  onMove,
}: {
  edge: Handle;
  shown: MsRange;
  fps: number;
  duration: number;
  drag: React.RefObject<Handle | null>;
  pxOf: (event: { clientX: number }) => number;
  scale: TimelineScale;
  onMove: (edge: Handle, ms: number, snap: boolean) => void;
}) {
  const ms = edge === "start" ? shown.start_ms : shown.end_ms;
  return (
    <div
      role="slider"
      tabIndex={0}
      aria-label={edge === "start" ? "Selection start" : "Selection end"}
      aria-orientation="horizontal"
      aria-valuemin={0}
      aria-valuemax={duration}
      aria-valuenow={ms}
      aria-valuetext={formatTimecode(ms)}
      data-handle={edge}
      className={cn(
        "pointer-events-auto absolute inset-y-0 w-3.5 cursor-ew-resize touch-none outline-none",
        edge === "start" ? "-left-[9px]" : "-right-[9px]",
        "after:absolute after:inset-y-2 after:left-1/2 after:w-1.5 after:-translate-x-1/2 after:rounded-full after:bg-brand after:shadow-[0_0_0_2px_var(--surface-raised)] focus-visible:after:ring-2 focus-visible:after:ring-foreground",
      )}
      onPointerDown={(event) => {
        event.stopPropagation();
        event.currentTarget.setPointerCapture?.(event.pointerId);
        drag.current = edge;
      }}
      onPointerMove={(event) => {
        if (drag.current === edge) onMove(edge, pxToMs(pxOf(event), scale), !event.altKey);
      }}
      onPointerUp={() => {
        drag.current = null;
      }}
      onPointerCancel={() => {
        drag.current = null;
      }}
      onKeyDown={(event) => {
        if (event.key !== "ArrowLeft" && event.key !== "ArrowRight") return;
        event.preventDefault();
        const frames = (event.key === "ArrowRight" ? 1 : -1) * (event.shiftKey ? 10 : 1);
        onMove(edge, stepFrames(ms, fps, frames, duration), false);
      }}
    />
  );
}
