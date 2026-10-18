// Shapes the review screen reads. They mirror the timeline spec (docs/design/timeline-v1.md §2), the render's
// `time_map` (§6) and the `annotations` table (docs/03 §5). P3-06 and the annotations API will fill them for real;
// until then the screen runs on `sample-data.ts`.

import type { SpeedPoint } from "./lib/speed-ramp";

export type OutputFormat = "9:16" | "1:1" | "4:5" | "16:9";

/** A cut or transition between two main-track items. */
export type TransitionIn =
  | { type: "cut" }
  | {
      type: "crossfade" | "dip_to_black" | "whip" | "whip_left" | "whip_right" | "zoom_through" | "flash" | "punch";
      duration_ms: number;
    };

/** A camera move on a clip (`motion`, timeline-v1 section 2): `punch_in`, `push_in`, `pull_back`, `shake` or `handheld`. */
export interface MotionSpec {
  preset: string;
  /** For `punch_in` and `shake`: where in the clip, from its own first frame. */
  at_ms?: number;
  strength?: number;
}

/** The colour grade of a clip (`look`). `none` is not kept. */
export interface LookSpec {
  preset: string;
  match?: boolean;
}

/** What the clip's own sound does: `mute` (the default), `bed` (low ambience under the edit) or `full` (the programme). */
export interface ClipAudio {
  mode: "mute" | "bed" | "full";
  gain_db?: number;
}

/** A generated sound effect (an `sfx` item of the audio track). */
export interface SfxItem {
  id: string;
  /** The category: `whoosh`, `impact`, `riser`... */
  sfx: string;
  variant?: string;
  at_ms: number;
  gain_db?: number;
}

/** A point on the beat grid. */
export interface BeatMarker {
  at_ms: number;
  kind: "beat" | "downbeat" | "drop" | "section";
  strength?: number;
}

/** An item on the `main` video track (the subset of the spec the review screen needs). */
export interface VideoItem {
  id: string;
  /** TypeID with the `ast` prefix. */
  asset_id: string;
  /** Output start. Redundant with the durations on purpose (spec §2). */
  at_ms: number;
  source: { in_ms: number; out_ms: number };
  /** 0.25 to 4.0. For a clip with a `speed_ramp`, its average speed (source span over output duration). */
  speed: number;
  /** A speed that changes along the clip (P3-10); replaces `speed`. `at_ms` counts from `source.in_ms`. */
  speed_ramp?: SpeedPoint[];
  transition_in?: TransitionIn;
  motion?: MotionSpec;
  look?: LookSpec;
  /** Always set by the spec reader; a clip with no audio settings is `mute`. Hand-made items may leave it out. */
  audio?: ClipAudio;
  /** Why the editor picked this clip (at most 280 characters, never rendered). */
  why?: string;
}

/** `time_map` entry for a main-track item. */
export interface MainTimeMapEntry {
  out_start_ms: number;
  out_end_ms: number;
  item_id: string;
  asset_id: string;
  source_in_ms: number;
  /** The clip's speed, or its average speed when it has a `speed_ramp`. */
  speed: number;
  speed_ramp?: SpeedPoint[];
}

/** `time_map` entry for a text item. Comments are mapped through main entries only. */
export interface TextTimeMapEntry {
  out_start_ms: number;
  out_end_ms: number;
  item_id: string;
}

export type TimeMapEntry = MainTimeMapEntry | TextTimeMapEntry;

/** One immutable timeline version together with the render that was made from it. */
export interface ReviewVersion {
  version: number;
  created_at: string;
  author_kind: "agent" | "user";
  /** One line, from `Summarize(diff)`. */
  summary: string;
  output: { format: OutputFormat; fps: number };
  duration_ms: number;
  items: VideoItem[];
  /** The generated sound effects, when the version has any. */
  sfx?: SfxItem[];
  /** The beat grid and the other markers of the version. */
  markers?: BeatMarker[];
  time_map: TimeMapEntry[];
  /** The preview render. Without one the screen plays a generated placeholder. */
  preview_url?: string;
  /** The version's spec has not arrived: it is listed, but has no clips yet (so comments cannot be moved through it). */
  loading?: boolean;
}

/** A normalised point on the frame: x and y in [0, 1], from the top left. */
export interface Pin {
  x: number;
  y: number;
}

export type AnnotationKind = "comment" | "include" | "exclude" | "prefer" | "instruction";
export type AnnotationStatus = "open" | "applied" | "resolved";

export interface CommentAuthor {
  kind: "creator" | "agent";
  name: string;
  /** The signed-in user wrote it: only they can edit or delete it. */
  own?: boolean;
}

/**
 * A comment on a render. `start_ms` and `end_ms` are output times on `version`'s render; a point comment has
 * `end_ms === start_ms`. An agent reply sets `reply_to` and takes its range and status from the comment it answers.
 */
export interface ReviewAnnotation {
  id: string;
  /** The version the comment was made on. */
  version: number;
  start_ms: number;
  end_ms: number;
  pin?: Pin;
  kind: AnnotationKind;
  body: string;
  status: AnnotationStatus;
  author: CommentAuthor;
  reply_to?: string;
  /** The version whose editor run applied this comment. */
  applied_in_version?: number;
  /** ISO 8601. */
  created_at: string;
}

export interface MsRange {
  start_ms: number;
  end_ms: number;
}

/** The comment being drafted: a range (or a point, `end_ms === start_ms`) with an optional pin. */
export interface CommentDraft extends MsRange {
  pin?: Pin;
}
