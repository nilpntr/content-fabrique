import { Film, Info, X } from "lucide-react";
import type * as React from "react";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import { AUDIO_MODES, EffectIcon, effectName, LookIcon, lookName, MOTION, RampIcon, SFX, TRANSITIONS } from "./effects";
import { msToPx, type TimelineScale } from "./lib/scale";
import { formatTimecode } from "./lib/time";
import type { BeatMarker, SfxItem, VideoItem } from "./types";

/** What is picked on the timeline to read about. A clip's badge names the aspect it stands for, so the details put it first. */
export type Focus =
  | { kind: "clip"; id: string; aspect?: "motion" | "speed" | "look" | "audio" }
  | { kind: "transition"; id: string }
  | { kind: "sfx"; id: string };

export const sameFocus = (a: Focus | null, b: Focus | null): boolean => a?.kind === b?.kind && a?.id === b?.id && (a?.kind !== "clip" || a.aspect === (b as typeof a).aspect);

/** Wires a mark on the timeline to the details line: hover or focus peeks at it, a click keeps it. */
export interface FocusControl {
  selected: Focus | null;
  onPeek: (focus: Focus | null) => void;
  onSelect: (focus: Focus) => void;
}

/** The pointer-down of a mark must not start a comment selection on the tracks below it. */
const stop = (event: React.PointerEvent) => event.stopPropagation();

function markProps(control: FocusControl, focus: Focus) {
  return {
    onPointerDown: stop,
    onClick: () => control.onSelect(focus),
    onPointerEnter: () => control.onPeek(focus),
    onPointerLeave: () => control.onPeek(null),
    onFocus: () => control.onPeek(focus),
    onBlur: () => control.onPeek(null),
    "aria-pressed": sameFocus(control.selected, focus),
  } as const;
}

const BADGE =
  "inline-flex h-[18px] min-w-[18px] shrink-0 cursor-pointer items-center justify-center gap-1 rounded-[5px] bg-black/50 px-1 text-[10px] leading-none font-medium text-white outline-none backdrop-blur-[2px] transition-colors hover:bg-black/70 focus-visible:ring-2 focus-visible:ring-white aria-pressed:bg-white aria-pressed:text-black";

/** The effects of a clip as small badges along its bottom edge: the camera move, a speed ramp, the sound mode and the grade. */
export function ClipBadges({ item, control }: { item: VideoItem; control: FocusControl }) {
  const mode = item.audio?.mode ?? "mute";
  const motion = item.motion;
  return (
    <div className="flex gap-1 overflow-hidden" data-slot="clip-badges">
      {motion ? (
        <button type="button" className={BADGE} aria-label={`${item.id}: ${effectName(MOTION, motion.preset)}`} data-badge="motion" {...markProps(control, { kind: "clip", id: item.id, aspect: "motion" })}>
          <EffectIcon table={MOTION} name={motion.preset} className="size-3" />
        </button>
      ) : null}
      {item.speed_ramp ? (
        <button type="button" className={BADGE} aria-label={`${item.id}: speed ramp`} data-badge="ramp" {...markProps(control, { kind: "clip", id: item.id, aspect: "speed" })}>
          <RampIcon className="size-3" />
        </button>
      ) : null}
      <button
        type="button"
        className={cn(BADGE, mode === "mute" && "text-white/60")}
        aria-label={`${item.id}: sound ${AUDIO_MODES[mode].label.toLowerCase()}`}
        data-badge="audio"
        data-mode={mode}
        {...markProps(control, { kind: "clip", id: item.id, aspect: "audio" })}
      >
        <EffectIcon table={AUDIO_MODES} name={mode} className="size-3" />
      </button>
      {item.look ? (
        <button type="button" className={cn(BADGE, "px-1.5")} aria-label={`${item.id}: look ${lookName(item.look.preset)}`} data-badge="look" {...markProps(control, { kind: "clip", id: item.id, aspect: "look" })}>
          <LookIcon aria-hidden className="size-3 shrink-0" />
          <span className="truncate">{item.look.preset === "none" ? "Match" : lookName(item.look.preset)}</span>
        </button>
      ) : null}
    </div>
  );
}

/** The id chip of a clip: the way in to the clip's details, and the editor's note on why it chose it. */
export function ClipHandle({ item, control }: { item: VideoItem; control: FocusControl }) {
  return (
    <button
      type="button"
      aria-label={`Details of ${item.id}`}
      data-handle={item.id}
      className="shrink-0 cursor-pointer rounded-[4px] bg-black/35 px-1 py-px font-mono text-[10px] leading-[14px] text-white/90 outline-none transition-colors hover:bg-black/60 focus-visible:ring-2 focus-visible:ring-white aria-pressed:bg-white aria-pressed:text-black"
      {...markProps(control, { kind: "clip", id: item.id })}
    >
      {item.id}
    </button>
  );
}

/** The blends between clips: a band over the overlap with the effect's icon at its middle. */
export function TransitionMarks({ items, scale, height, control }: { items: readonly VideoItem[]; scale: TimelineScale; height: number; control: FocusControl }) {
  return (
    <>
      {items.map((item) => {
        const transition = item.transition_in;
        if (!transition || transition.type === "cut") return null;
        const left = msToPx(item.at_ms, scale);
        const width = Math.max(msToPx(transition.duration_ms, scale), 4);
        const name = effectName(TRANSITIONS, transition.type);
        return (
          <div key={item.id} data-transition={item.id} className="pointer-events-none absolute top-0 z-[5]" style={{ left, width, height }}>
            <span aria-hidden className="absolute inset-0 rounded-[3px] bg-white/25 shadow-[inset_0_0_0_1px_rgba(255,255,255,0.35)]" />
            <button
              type="button"
              aria-label={`${name} into ${item.id}`}
              data-mark="transition"
              className={cn(BADGE, "pointer-events-auto absolute top-1/2 left-1/2 size-[22px] -translate-x-1/2 -translate-y-1/2 rounded-full bg-black/70 shadow-[0_0_0_1.5px_rgba(255,255,255,0.8)]")}
              {...markProps(control, { kind: "transition", id: item.id })}
            >
              <EffectIcon table={TRANSITIONS} name={transition.type} className="size-3" />
            </button>
          </div>
        );
      })}
    </>
  );
}

/** Points this far apart (px) share a row. */
const SFX_PX = 20;
export const SFX_ROW_H = 22;
export const SFX_PAD = 3;

/** Rows for the effects so that two close in time do not cover each other: the first row that is free. */
export function sfxRows(sfx: readonly SfxItem[], scale: TimelineScale): { rows: Map<string, number>; count: number } {
  const rows = new Map<string, number>();
  const ends: number[] = [];
  for (const item of [...sfx].sort((a, b) => a.at_ms - b.at_ms || a.id.localeCompare(b.id))) {
    const left = msToPx(item.at_ms, scale);
    let row = ends.findIndex((end) => end <= left);
    if (row === -1) row = ends.length;
    ends[row] = left + SFX_PX;
    rows.set(item.id, row);
  }
  return { rows, count: Math.max(ends.length, 1) };
}

export const sfxLaneHeight = (rowCount: number): number => rowCount * SFX_ROW_H + SFX_PAD * 2;

const sfxFullName = (item: SfxItem): string => `${effectName(SFX, item.sfx)}${item.variant ? ` (${item.variant.replaceAll("_", " ")})` : ""}`;

/** The sound-effects lane: one icon per effect at the moment it starts. */
export function SfxLane({ sfx, scale, rows, control }: { sfx: readonly SfxItem[]; scale: TimelineScale; rows: Map<string, number>; control: FocusControl }) {
  return (
    <>
      {sfx.map((item) => (
        <button
          key={item.id}
          type="button"
          aria-label={`${sfxFullName(item)} at ${formatTimecode(item.at_ms)}`}
          title={`${sfxFullName(item)} · ${formatTimecode(item.at_ms)}`}
          data-sfx={item.id}
          data-category={item.sfx}
          className={cn(
            "absolute flex size-[18px] -translate-x-0 cursor-pointer items-center justify-center rounded-md border border-brand/50 bg-brand/20 text-foreground outline-none transition-colors hover:bg-brand/40 focus-visible:ring-2 focus-visible:ring-brand aria-pressed:bg-brand aria-pressed:text-primary-foreground",
          )}
          style={{ left: msToPx(item.at_ms, scale), top: SFX_PAD + (rows.get(item.id) ?? 0) * SFX_ROW_H }}
          {...markProps(control, { kind: "sfx", id: item.id })}
        >
          <EffectIcon table={SFX} name={item.sfx} className="size-3" />
        </button>
      ))}
    </>
  );
}

/** Beats further apart than this (px) are all drawn; closer, only the downbeats. */
const MIN_BEAT_PX = 5;

/** The beat grid: ticks at the foot of the ruler (taller on a downbeat), and faint lines through the clips for the downbeats. */
export function BeatTicks({ markers, scale }: { markers: readonly BeatMarker[]; scale: TimelineScale }) {
  const grid = markers.filter((marker) => marker.kind === "beat" || marker.kind === "downbeat");
  // The distance between neighbours on screen decides how much of the grid is drawn: at a small zoom only the downbeats.
  const gap = grid.length > 1 ? msToPx((grid.at(-1)?.at_ms ?? 0) - (grid[0]?.at_ms ?? 0), scale) / (grid.length - 1) : Number.POSITIVE_INFINITY;
  const showBeats = gap >= MIN_BEAT_PX;
  return (
    <>
      {grid.map((marker) =>
        marker.kind === "beat" && !showBeats ? null : (
          <div
            key={`${marker.kind}-${marker.at_ms}`}
            aria-hidden
            data-beat={marker.kind}
            className={cn("pointer-events-none absolute bottom-0 w-px", marker.kind === "downbeat" ? "h-3 bg-brand" : "h-1.5 bg-brand/45")}
            style={{ left: msToPx(marker.at_ms, scale) }}
          />
        ),
      )}
    </>
  );
}

/** The downbeats as faint lines down the clips and the sound lane, so a cut on the beat can be seen. */
export function DownbeatLines({ markers, scale, height }: { markers: readonly BeatMarker[]; scale: TimelineScale; height: number }) {
  return (
    <>
      {markers
        .filter((marker) => marker.kind === "downbeat")
        .map((marker) => (
          <div key={marker.at_ms} aria-hidden data-downbeat-line={marker.at_ms} className="pointer-events-none absolute top-0 w-px bg-white/25" style={{ left: msToPx(marker.at_ms, scale), height }} />
        ))}
    </>
  );
}

// ---- The details line --------------------------------------------------------------------------------------------------

const sec = (ms: number): string => formatTimecode(ms);

function Fact({ label, children, emphasis }: { label: string; children: React.ReactNode; emphasis?: boolean }) {
  return (
    <div className={cn("flex min-w-0 items-baseline gap-1.5 rounded-md px-1.5 py-0.5", emphasis && "bg-brand-wash")}>
      <dt className="text-subtle-foreground">{label}</dt>
      <dd className="min-w-0 truncate font-medium">{children}</dd>
    </div>
  );
}

function ClipDetails({ item, title, aspect }: { item: VideoItem; title?: string; aspect?: string }) {
  const motion = item.motion;
  const mode = AUDIO_MODES[item.audio?.mode ?? "mute"];
  return (
    <>
      <p className="flex min-w-0 items-center gap-2">
        <Film aria-hidden className="size-3.5 shrink-0 text-muted-foreground" />
        <span className="min-w-0 truncate font-medium">
          <span className="font-mono text-[11.5px] text-muted-foreground">{item.id}</span> {title ?? item.asset_id}
        </span>
      </p>
      <dl className="mt-1 flex flex-wrap gap-x-2 gap-y-0.5">
        <Fact label="Source">
          {sec(item.source.in_ms)}–{sec(item.source.out_ms)}
        </Fact>
        <Fact label="Speed" emphasis={aspect === "speed"}>
          {item.speed_ramp ? `Ramp, ${item.speed.toFixed(2)}× on average` : `${item.speed}×`}
        </Fact>
        {motion ? (
          <Fact label="Camera" emphasis={aspect === "motion"}>
            {effectName(MOTION, motion.preset)}
            {motion.at_ms !== undefined ? ` at ${sec(motion.at_ms)}` : ""}
            {motion.strength !== undefined ? `, ${Math.round(motion.strength * 100)}%` : ""}
          </Fact>
        ) : null}
        <Fact label="Look" emphasis={aspect === "look"}>
          {item.look ? `${item.look.preset === "none" ? "Brightness match" : lookName(item.look.preset)}${item.look.match && item.look.preset !== "none" ? ", brightness matched" : ""}` : "None"}
        </Fact>
        <Fact label="Sound" emphasis={aspect === "audio"}>
          {mode.label}
          {item.audio?.gain_db ? `, ${item.audio.gain_db > 0 ? "+" : ""}${item.audio.gain_db} dB` : ""}
        </Fact>
      </dl>
      {aspect === "audio" ? <p className="mt-1 text-muted-foreground">{mode.hint}</p> : null}
      {aspect === "motion" && motion ? <p className="mt-1 text-muted-foreground">{MOTION[motion.preset]?.hint}</p> : null}
      {item.why ? (
        <p className="mt-1.5 max-w-prose text-pretty">
          <span className="text-subtle-foreground">Why: </span>
          {item.why}
        </p>
      ) : (
        <p className="mt-1.5 text-subtle-foreground">The editor left no note for this clip.</p>
      )}
    </>
  );
}

/** What a mark is, as one sentence for a screen reader. */
function spokenFocus(focus: Focus | null, items: readonly VideoItem[], sfx: readonly SfxItem[], assetNames?: Record<string, string>): string {
  if (!focus) return "";
  if (focus.kind === "sfx") {
    const effect = sfx.find((candidate) => candidate.id === focus.id);
    return effect ? `${effectName(SFX, effect.sfx)}${effect.variant ? `, ${effect.variant.replaceAll("_", " ")} variant` : ""}, at ${sec(effect.at_ms)}` : "";
  }
  const index = items.findIndex((candidate) => candidate.id === focus.id);
  const item = items[index];
  if (!item) return "";
  if (focus.kind === "transition") {
    const transition = item.transition_in;
    if (!transition || transition.type === "cut") return "";
    const previous = items[index - 1];
    return `${effectName(TRANSITIONS, transition.type)}, ${transition.duration_ms} milliseconds${previous ? `, between ${previous.id} and ${item.id}` : ""}`;
  }
  const parts = [`${item.id} ${assetNames?.[item.asset_id] ?? ""}`.trim(), `sound ${AUDIO_MODES[item.audio?.mode ?? "mute"].label.toLowerCase()}`];
  if (item.motion) parts.push(effectName(MOTION, item.motion.preset));
  if (item.speed_ramp) parts.push("speed ramp");
  if (item.look) parts.push(`look ${lookName(item.look.preset)}`);
  if (item.why) parts.push(`why: ${item.why}`);
  return parts.join(", ");
}

/**
 * The line under the timeline that says what a mark is. Hovering or focusing a mark shows it here; a click keeps it (`pinnedFocus`).
 * Its height is reserved, so showing a mark does not push the page down. A screen reader is told about the kept mark only: pointing
 * at marks along the timeline must not talk.
 */
export function TimelineDetails({
  focus,
  pinnedFocus,
  items,
  sfx,
  assetNames,
  onClose,
}: {
  /** What is shown: the mark under the pointer, else the one that was kept. */
  focus: Focus | null;
  pinnedFocus: Focus | null;
  items: readonly VideoItem[];
  sfx: readonly SfxItem[];
  assetNames?: Record<string, string>;
  onClose: () => void;
}) {
  const item = focus && focus.kind !== "sfx" ? items.find((candidate) => candidate.id === focus.id) : undefined;
  const effect = focus?.kind === "sfx" ? sfx.find((candidate) => candidate.id === focus.id) : undefined;
  const index = item ? items.indexOf(item) : -1;
  const transition = focus?.kind === "transition" && item?.transition_in && item.transition_in.type !== "cut" ? item.transition_in : undefined;
  const previous = index > 0 ? items[index - 1] : undefined;
  // The close button is for the kept mark, whenever that is the one shown.
  const pinned = pinnedFocus !== null && sameFocus(focus, pinnedFocus);
  const spoken = spokenFocus(pinnedFocus, items, sfx, assetNames);

  return (
    <>
      <p role="status" className="sr-only" data-testid="timeline-announcement">
        {spoken}
      </p>
    <section aria-label="Details" data-testid="timeline-details" className="relative min-h-[76px] rounded-lg bg-well px-3 py-2 text-[12.5px] leading-[1.45] shadow-well sm:min-h-[68px]">
      {focus && (item || effect) ? (
        <>
          {focus.kind === "clip" && item ? <ClipDetails item={item} title={assetNames?.[item.asset_id]} aspect={focus.aspect} /> : null}
          {transition && item ? (
            <>
              <p className="flex items-center gap-2 font-medium">
                <EffectIcon table={TRANSITIONS} name={transition.type} className="size-3.5 text-muted-foreground" />
                {effectName(TRANSITIONS, transition.type)}
                <span className="font-normal text-muted-foreground tabular-nums">{transition.duration_ms} ms</span>
              </p>
              <p className="mt-1 text-muted-foreground">
                {previous ? `Between ${previous.id} and ${item.id}. ` : ""}
                {TRANSITIONS[transition.type]?.hint}
              </p>
            </>
          ) : null}
          {effect ? (
            <>
              <p className="flex items-center gap-2 font-medium">
                <EffectIcon table={SFX} name={effect.sfx} className="size-3.5 text-muted-foreground" />
                {effectName(SFX, effect.sfx)}
                {effect.variant ? <span className="font-normal text-muted-foreground">{effect.variant.replaceAll("_", " ")} variant</span> : null}
                <span className="font-normal text-muted-foreground tabular-nums">at {sec(effect.at_ms)}</span>
              </p>
              <p className="mt-1 text-muted-foreground">
                {SFX[effect.sfx]?.hint}
                {effect.gain_db ? ` ${effect.gain_db > 0 ? "+" : ""}${effect.gain_db} dB.` : ""}
              </p>
            </>
          ) : null}
          {pinned ? (
            <Button variant="ghost" size="icon-sm" aria-label="Close details" className="absolute top-1.5 right-1.5" onClick={onClose}>
              <X />
            </Button>
          ) : null}
        </>
      ) : (
        <p className="flex items-start gap-2 text-subtle-foreground">
          <Info aria-hidden className="mt-0.5 size-3.5 shrink-0" />
          Point at an effect, a transition or a sound to see what it does, and why the editor chose the clip.
        </p>
      )}
    </section>
    </>
  );
}
