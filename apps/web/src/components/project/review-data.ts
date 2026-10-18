import type { Render, TimelineVersion, TimelineVersionSummary } from "@/lib/api/client";
import { buildTimeMap, isMainEntry, itemOutputDurationMs } from "@/components/editor/lib/time-map";
import { rampOutMs, type SpeedPoint } from "@/components/editor/lib/speed-ramp";
import type { BeatMarker, ClipAudio, LookSpec, MotionSpec, OutputFormat, ReviewVersion, SfxItem, TimeMapEntry, TransitionIn, VideoItem } from "@/components/editor/types";

/**
 * From the API's timeline versions (and renders) to what the review screen shows. The spec is read defensively: it comes from
 * the server as untyped JSON (`packages/schemas/timeline/v1.schema.json`), and a field that is not what the schema says is left
 * out and never throws.
 */

const isRecord = (value: unknown): value is Record<string, unknown> => typeof value === "object" && value !== null && !Array.isArray(value);
const num = (value: unknown): number | undefined => (typeof value === "number" && Number.isFinite(value) ? value : undefined);

type Blend = Exclude<TransitionIn["type"], "cut">;
const BLENDS: ReadonlySet<string> = new Set<Blend>(["crossfade", "dip_to_black", "whip", "whip_left", "whip_right", "zoom_through", "flash"]);
const isBlend = (value: unknown): value is Blend => typeof value === "string" && BLENDS.has(value);

/** The points of a speed ramp (P3-10), or undefined when `value` is not a usable one. */
function rampOf(value: unknown): SpeedPoint[] | undefined {
  if (!Array.isArray(value) || value.length < 2) return undefined;
  const points: SpeedPoint[] = [];
  for (const raw of value) {
    const at = isRecord(raw) ? num(raw.at_ms) : undefined;
    const speed = isRecord(raw) ? num(raw.speed) : undefined;
    if (at === undefined || speed === undefined || speed <= 0) return undefined;
    points.push({ at_ms: at, speed });
  }
  return points;
}

function transitionOf(value: unknown): TransitionIn | undefined {
  if (!isRecord(value)) return undefined;
  if (value.type === "cut") return { type: "cut" };
  const duration = num(value.duration_ms);
  if (isBlend(value.type) && duration !== undefined) {
    return { type: value.type, duration_ms: duration };
  }
  return undefined;
}

/** The clips of the main video track, in time order. */
export function mainItems(spec: unknown): VideoItem[] {
  if (!isRecord(spec) || !Array.isArray(spec.tracks)) return [];
  const track = spec.tracks.find((candidate) => isRecord(candidate) && candidate.type === "video");
  if (!isRecord(track) || !Array.isArray(track.items)) return [];
  const items: VideoItem[] = [];
  for (const raw of track.items) {
    if (!isRecord(raw) || typeof raw.id !== "string" || typeof raw.asset_id !== "string" || !isRecord(raw.source)) continue;
    const at = num(raw.at_ms);
    const inMs = num(raw.source.in_ms);
    const outMs = num(raw.source.out_ms);
    if (at === undefined || inMs === undefined || outMs === undefined) continue;
    const transition = transitionOf(raw.transition_in);
    const ramp = rampOf(raw.speed_ramp);
    // A ramp replaces the speed; `speed` then holds the average, which is what readers that know nothing of ramps use.
    const rampMs = ramp ? rampOutMs(ramp, outMs - inMs) : 0;
    const speed = ramp && rampMs > 0 ? (outMs - inMs) / rampMs : (num(raw.speed) ?? 1);
    const motion = motionOf(raw.motion);
    const look = lookOf(raw.look);
    const why = typeof raw.why === "string" && raw.why.trim() !== "" ? raw.why.trim() : undefined;
    items.push({
      id: raw.id,
      asset_id: raw.asset_id,
      at_ms: at,
      source: { in_ms: inMs, out_ms: outMs },
      speed,
      ...(ramp ? { speed_ramp: ramp } : {}),
      ...(transition ? { transition_in: transition } : {}),
      ...(motion ? { motion } : {}),
      ...(look ? { look } : {}),
      audio: audioOf(raw.audio),
      ...(why ? { why } : {}),
    });
  }
  return items.sort((a, b) => a.at_ms - b.at_ms);
}

/** A camera move, or undefined for none (`preset: "none"` is no move). */
function motionOf(value: unknown): MotionSpec | undefined {
  if (!isRecord(value) || typeof value.preset !== "string" || value.preset === "none") return undefined;
  const at = num(value.at_ms);
  const strength = num(value.strength);
  return { preset: value.preset, ...(at !== undefined ? { at_ms: at } : {}), ...(strength !== undefined ? { strength } : {}) };
}

/** A colour grade, or undefined for none. A clip with `match` and no preset still has a look (the exposure match). */
function lookOf(value: unknown): LookSpec | undefined {
  if (!isRecord(value) || typeof value.preset !== "string") return undefined;
  const match = value.match === true;
  if (value.preset === "none" && !match) return undefined;
  return { preset: value.preset, ...(match ? { match } : {}) };
}

/**
 * What the clip's own sound does. `audio.mode` is `mute`, `bed` or `full`; the older spelling is `use_source` (true is `full`).
 * A clip with no audio settings is silent.
 */
function audioOf(value: unknown): ClipAudio {
  if (!isRecord(value)) return { mode: "mute" };
  const gain = num(value.gain_db);
  const mode = value.mode === "mute" || value.mode === "bed" || value.mode === "full" ? value.mode : value.use_source === true ? "full" : "mute";
  return { mode, ...(gain !== undefined && gain !== 0 ? { gain_db: gain } : {}) };
}

/** The generated sound effects of the audio track, in time order. Other kinds of audio (a trend sound, a track) are not drawn. */
export function sfxItems(spec: unknown): SfxItem[] {
  if (!isRecord(spec) || !Array.isArray(spec.tracks)) return [];
  const out: SfxItem[] = [];
  for (const track of spec.tracks) {
    if (!isRecord(track) || track.type !== "audio" || !Array.isArray(track.items)) continue;
    for (const raw of track.items) {
      if (!isRecord(raw) || raw.kind !== "sfx" || typeof raw.id !== "string" || typeof raw.sfx !== "string") continue;
      const at = num(raw.at_ms);
      if (at === undefined) continue;
      const gain = num(raw.gain_db);
      out.push({ id: raw.id, sfx: raw.sfx, at_ms: at, ...(typeof raw.variant === "string" ? { variant: raw.variant } : {}), ...(gain !== undefined ? { gain_db: gain } : {}) });
    }
  }
  return out.sort((a, b) => a.at_ms - b.at_ms);
}

const MARKER_KINDS: ReadonlySet<string> = new Set(["beat", "downbeat", "drop", "section"]);

/** The markers of the spec (the beat grid and the rest), in time order. */
export function markersOf(spec: unknown): BeatMarker[] {
  if (!isRecord(spec) || !Array.isArray(spec.markers)) return [];
  const out: BeatMarker[] = [];
  for (const raw of spec.markers) {
    if (!isRecord(raw) || typeof raw.kind !== "string" || !MARKER_KINDS.has(raw.kind)) continue;
    const at = num(raw.at_ms);
    if (at === undefined) continue;
    const strength = num(raw.strength);
    // One marker of a kind at a time: a duplicate would be drawn twice.
    if (out.some((marker) => marker.at_ms === at && marker.kind === raw.kind)) continue;
    out.push({ at_ms: at, kind: raw.kind as BeatMarker["kind"], ...(strength !== undefined ? { strength } : {}) });
  }
  return out.sort((a, b) => a.at_ms - b.at_ms);
}

/** The texts on screen (the `text` tracks: titles, captions), in time order. */
export function textItems(spec: unknown): { at_ms: number; text: string }[] {
  if (!isRecord(spec) || !Array.isArray(spec.tracks)) return [];
  const out: { at_ms: number; text: string }[] = [];
  for (const track of spec.tracks) {
    if (!isRecord(track) || track.type !== "text" || !Array.isArray(track.items)) continue;
    for (const raw of track.items) {
      const at = isRecord(raw) ? num(raw.at_ms) : undefined;
      if (at === undefined || !isRecord(raw) || typeof raw.text !== "string" || raw.text.trim() === "") continue;
      out.push({ at_ms: at, text: raw.text.trim() });
    }
  }
  return out.sort((a, b) => a.at_ms - b.at_ms);
}

/** Where the last clip ends: the length of the video. */
export function specDurationMs(items: readonly VideoItem[]): number {
  return items.reduce((end, item) => Math.max(end, item.at_ms + itemOutputDurationMs(item)), 0);
}

const FORMATS: ReadonlySet<string> = new Set(["9:16", "1:1", "4:5", "16:9"]);

function outputOf(spec: unknown, fallback: OutputFormat): { format: OutputFormat; fps: number } {
  const output = isRecord(spec) && isRecord(spec.output) ? spec.output : {};
  const format = typeof output.format === "string" && FORMATS.has(output.format) ? (output.format as OutputFormat) : fallback;
  return { format, fps: num(output.fps) ?? 30 };
}

/** What the render's time map (a JSON file) holds, as far as the review uses it. Anything else in it is ignored. */
export function parseTimeMap(value: unknown): TimeMapEntry[] | undefined {
  if (!Array.isArray(value)) return undefined;
  const entries: TimeMapEntry[] = [];
  for (const raw of value) {
    if (!isRecord(raw) || typeof raw.item_id !== "string") continue;
    const start = num(raw.out_start_ms);
    const end = num(raw.out_end_ms);
    if (start === undefined || end === undefined) continue;
    if (typeof raw.asset_id === "string") {
      const ramp = rampOf(raw.speed_ramp);
      entries.push({ out_start_ms: start, out_end_ms: end, item_id: raw.item_id, asset_id: raw.asset_id, source_in_ms: num(raw.source_in_ms) ?? 0, speed: num(raw.speed) ?? 1, ...(ramp ? { speed_ramp: ramp } : {}) });
    } else {
      entries.push({ out_start_ms: start, out_end_ms: end, item_id: raw.item_id });
    }
  }
  return entries;
}

/** The one line the version selector shows: the changes of the version, or who made it. */
export function versionSummaryLine(summary: TimelineVersionSummary): string {
  const lines = Array.isArray(summary.summary) ? summary.summary.filter((line) => line.trim() !== "") : [];
  if (lines.length > 0) return lines.join("; ");
  return summary.author_kind === "agent" ? "Edited by the editor" : "Edited by you";
}

export interface VersionInputs {
  summary: TimelineVersionSummary;
  /** Null until the version's spec has been fetched. */
  version: TimelineVersion | null;
  /** The ready preview render of this version, when there is one. */
  render?: Render;
  /** The render's time map, when it could be read. */
  renderTimeMap?: TimeMapEntry[];
  /** The URL of the render's MP4. */
  previewUrl?: string;
}

/** A listed version whose spec has not been fetched: it can be chosen, and shows as loading until the spec arrives. */
export function loadingReviewVersion(summary: TimelineVersionSummary, projectFormat: OutputFormat, duration_ms = 1000): ReviewVersion {
  return {
    version: summary.version,
    created_at: summary.created_at,
    author_kind: summary.author_kind,
    summary: versionSummaryLine(summary),
    output: { format: projectFormat, fps: 30 },
    duration_ms,
    items: [],
    time_map: [],
    loading: true,
  };
}

/** A version for the review screen, or null while its spec is not there yet. Without a render, the time map is the one the renderer would write. */
export function toReviewVersion(input: VersionInputs, projectFormat: OutputFormat): ReviewVersion | null {
  if (!input.version) return null;
  const items = mainItems(input.version.spec);
  const derived = buildTimeMap(items);
  const timeMap = input.renderTimeMap?.some(isMainEntry) ? input.renderTimeMap : derived;
  const duration = input.render?.duration_ms ?? specDurationMs(items);
  return {
    version: input.summary.version,
    created_at: input.summary.created_at,
    author_kind: input.summary.author_kind,
    summary: versionSummaryLine(input.summary),
    output: outputOf(input.version.spec, projectFormat),
    duration_ms: Math.max(duration, 1),
    items,
    sfx: sfxItems(input.version.spec),
    markers: markersOf(input.version.spec),
    texts: textItems(input.version.spec),
    time_map: timeMap,
    ...(input.previewUrl ? { preview_url: input.previewUrl } : {}),
  };
}
