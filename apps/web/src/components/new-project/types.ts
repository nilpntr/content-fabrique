/** Mirrors the future API (D-25): the four output sizes a project can pick. */
export const FORMAT_IDS = ["9:16", "1:1", "4:5", "16:9"] as const;
export type Format = (typeof FORMAT_IDS)[number];

import type { Facet } from "@/lib/facets";

/** A TypeID: the `ast_` prefix plus a base32 UUIDv7 (D-24). */
export type AssetId = `ast_${string}`;

/** A clip in the library, as the library list endpoint will return it. */
export interface Asset {
  id: AssetId;
  title: string;
  /** A still of the clip. A clip without one shows a plain box. */
  posterUrl?: string;
  /** A short muted loop, played while the pointer rests on the card. */
  hoverPreviewUrl?: string;
  durationMs: number;
  /** The exercise the quick look named ("seated cable row"); the card's heading unless the creator named the clip. */
  exercise?: string;
  /** `user` when the creator named the clip: that name is the heading then. */
  titleSource?: string;
  /** The clip's labels (the type, the body part, the angle...). */
  facets: Facet[];
}

/** What "Create project" hands to the caller. `assetIds` are in the order the clips were picked. */
export interface NewProjectInput {
  format: Format;
  assetIds: AssetId[];
  /** The trimmed free-text instructions; an empty string when the creator wrote none. */
  brief: string;
  /** `max` for an extra careful first edit; left out for a standard one. */
  quality?: "max";
  /** The trimmed project name. Left out when the creator gave none (the server names the project). */
  title?: string;
  /** The vibe picked for this video; left out when the creator kept their style's own. */
  vibe?: "calm" | "lively" | "hype";
}
