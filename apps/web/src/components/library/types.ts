/** Mirrors the future library API (P1-01 / P0-08). Until it lands the components run on `sample-data.ts`. */

/** A TypeID: the `ast_` prefix plus a base32 UUIDv7 (D-24). */
export type AssetId = `ast_${string}`;

export const ASSET_STATUSES = ["uploading", "uploaded", "processing", "ready", "failed"] as const;
export type AssetStatus = (typeof ASSET_STATUSES)[number];

/** A sprite sheet laid out on a regular grid: tile `n` is the frame at `n * intervalMs`, row by row. */
export interface SpriteGrid {
  cols: number;
  rows: number;
  intervalMs: number;
  tileW: number;
  tileH: number;
}

/** The scrub sprite: the sheet image plus how to find a frame in it, either a regular grid or a WebVTT thumbnails track. */
export type Sprite = { url: string } & ({ grid: SpriteGrid; vttUrl?: undefined } | { vttUrl: string; grid?: undefined });

export interface Asset {
  id: AssetId;
  title: string;
  description?: string;
  tags: string[];
  status: AssetStatus;
  /** 0 to 100, while `uploading` or `processing`. Left out when unknown, which shows an indeterminate bar. */
  progress?: number;
  durationMs: number;
  /** The source size in pixels; the card keeps this orientation inside its fixed ratio. */
  width: number;
  height: number;
  /** ISO 8601 timestamp. */
  createdAt: string;
  posterUrl?: string;
  /** A short muted loop. Not requested until the pointer rests on the card. */
  hoverPreviewUrl?: string;
  sprite?: Sprite;
}
