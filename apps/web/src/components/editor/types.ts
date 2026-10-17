// Shapes the review screen reads. They mirror the timeline spec (docs/design/timeline-v1.md §2), the render's
// `time_map` (§6) and the `annotations` table (docs/03 §5). P3-06 and the annotations API will fill them for real;
// until then the screen runs on `sample-data.ts`.

export type OutputFormat = "9:16" | "1:1" | "4:5" | "16:9";

/** A cut or transition between two main-track items. */
export type TransitionIn = { type: "cut" } | { type: "crossfade" | "dip_to_black" | "whip"; duration_ms: number };

/** An item on the `main` video track (the subset of the spec the review screen needs). */
export interface VideoItem {
  id: string;
  /** TypeID with the `ast` prefix. */
  asset_id: string;
  /** Output start. Redundant with the durations on purpose (spec §2). */
  at_ms: number;
  source: { in_ms: number; out_ms: number };
  /** 0.25 to 4.0. */
  speed: number;
  transition_in?: TransitionIn;
}

/** `time_map` entry for a main-track item. */
export interface MainTimeMapEntry {
  out_start_ms: number;
  out_end_ms: number;
  item_id: string;
  asset_id: string;
  source_in_ms: number;
  speed: number;
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
