import {
  AudioLines,
  BellRing,
  Blend,
  Feather,
  Hammer,
  Maximize2,
  Minimize2,
  Moon,
  MousePointerClick,
  MoveLeft,
  MoveRight,
  Palette,
  ScanLine,
  Sparkles,
  TrendingDown,
  TrendingUp,
  Vibrate,
  Volume2,
  VolumeX,
  Wind,
  Zap,
  ZoomIn,
  type LucideIcon,
  ArrowDownToLine,
  Bubbles,
} from "lucide-react";
import type * as React from "react";
import { cn } from "@/lib/utils";

/**
 * What the editor can put on a clip, in the creator's words: one place for the icons, names and one-line explanations, so the
 * run feed (the plan names effects), the timeline (the icons sit on the clips) and the details line all say the same thing.
 * The names are the spec's (docs/design/timeline-v1.md section 2); a name this build does not know is shown as it is.
 */

export interface EffectMeta {
  label: string;
  /** What it does, in a sentence for the details line. */
  hint: string;
  Icon: LucideIcon;
}

export const MOTION: Readonly<Record<string, EffectMeta>> = {
  punch_in: { label: "Punch-in", hint: "A fast zoom that lands on a beat and holds.", Icon: ZoomIn },
  push_in: { label: "Push-in", hint: "A slow zoom in over the whole shot.", Icon: Maximize2 },
  pull_back: { label: "Pull-back", hint: "Starts zoomed in and pulls back to the full frame.", Icon: Minimize2 },
  shake: { label: "Shake", hint: "A short camera shake that dies out, for an impact.", Icon: Vibrate },
};

export const TRANSITIONS: Readonly<Record<string, EffectMeta>> = {
  crossfade: { label: "Crossfade", hint: "The two shots blend into each other.", Icon: Blend },
  dip_to_black: { label: "Dip to black", hint: "Fades out to black and back in.", Icon: Moon },
  whip: { label: "Whip", hint: "A fast slide with motion blur.", Icon: MoveLeft },
  whip_left: { label: "Whip left", hint: "A fast slide to the left with motion blur.", Icon: MoveLeft },
  whip_right: { label: "Whip right", hint: "A fast slide to the right with motion blur.", Icon: MoveRight },
  zoom_through: { label: "Zoom through", hint: "Zooms into the first shot and out of the next, through a blur.", Icon: ZoomIn },
  flash: { label: "Flash", hint: "A dip to white.", Icon: Zap },
};

export const SFX: Readonly<Record<string, EffectMeta>> = {
  whoosh: { label: "Whoosh", hint: "An air pass-by for a whip or a cut.", Icon: Wind },
  swish: { label: "Swish", hint: "A quick thin air pass for a small move.", Icon: Feather },
  impact: { label: "Impact", hint: "A low hit with a crack, for a punch-in or the drop.", Icon: Hammer },
  riser: { label: "Riser", hint: "A rising build that peaks at its end.", Icon: TrendingUp },
  downlifter: { label: "Downlifter", hint: "A falling release after a hit.", Icon: TrendingDown },
  sub_drop: { label: "Sub drop", hint: "A pitch-dropping low tone under a hit.", Icon: ArrowDownToLine },
  pop: { label: "Pop", hint: "A small bubble for something appearing.", Icon: Bubbles },
  click: { label: "Click", hint: "A UI click, on the frame of a press.", Icon: MousePointerClick },
  glitch: { label: "Glitch", hint: "A digital stutter for a glitchy cut.", Icon: ScanLine },
  shimmer: { label: "Shimmer", hint: "A sparkle for something new.", Icon: Sparkles },
  sting: { label: "Sting", hint: "A logo or reveal hit.", Icon: BellRing },
};

export type AudioMode = "mute" | "bed" | "full";

export const AUDIO_MODES: Readonly<Record<AudioMode, EffectMeta>> = {
  mute: { label: "Muted", hint: "The clip's own sound is off.", Icon: VolumeX },
  bed: { label: "Bed", hint: "The clip's own sound as a low ambience under the edit, evened out across clips.", Icon: AudioLines },
  full: { label: "Full sound", hint: "The clip's own sound at its own level, for speech.", Icon: Volume2 },
};

/** The colour grades (`look.preset`), as short names. `none` is not shown. */
export const LOOKS: Readonly<Record<string, string>> = {
  punchy: "Punchy",
  warm: "Warm",
  cool: "Cool",
  bw: "B&W",
  matte: "Matte",
  auto: "Auto grade",
  gym_moody: "Gym moody",
};

export const effectName = (table: Readonly<Record<string, EffectMeta>>, key: string): string => table[key]?.label ?? key.replaceAll("_", " ");

/** Look names shown as a chip; an unknown preset reads as its name. */
export const lookName = (preset: string): string => LOOKS[preset] ?? preset.replaceAll("_", " ");

export { Palette as LookIcon };

/** A speed ramp as a little curve: slow, fast, slow. Drawn at the size of its text. */
export function RampIcon({ className }: { className?: string }) {
  return (
    <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.75" strokeLinecap="round" strokeLinejoin="round" aria-hidden className={cn("size-3.5", className)}>
      <path d="M1.5 12.5C5 12.5 5.5 3.5 8 3.5S11 12.5 14.5 12.5" />
    </svg>
  );
}

/** The icon of an effect, by table and key, with a fallback for a name this build does not know. */
export function EffectIcon({ table, name, className }: { table: Readonly<Record<string, EffectMeta>>; name: string; className?: string }): React.ReactElement {
  const Icon = table[name]?.Icon ?? Sparkles;
  return <Icon aria-hidden className={cn("size-3.5", className)} />;
}
