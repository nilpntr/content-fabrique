import type { Format } from "./types";

export type FormatDevice = "phone" | "feed-post" | "laptop";

export interface FormatInfo {
  id: Format;
  name: string;
  /** Output size in pixels, "width×height". */
  size: string;
  use: string;
  device: FormatDevice;
}

/** D-25, in the order the cards appear. */
export const FORMATS: readonly FormatInfo[] = [
  { id: "9:16", name: "Vertical video", size: "1080×1920", use: "TikTok, Reels, Shorts", device: "phone" },
  { id: "1:1", name: "Square post", size: "1080×1080", use: "Feed post", device: "feed-post" },
  { id: "4:5", name: "Portrait post", size: "1080×1350", use: "Instagram portrait feed", device: "feed-post" },
  { id: "16:9", name: "Landscape video", size: "1920×1080", use: "YouTube, landscape", device: "laptop" },
];

export function formatInfo(id: Format): FormatInfo {
  const info = FORMATS.find((format) => format.id === id);
  if (!info) throw new Error(`Unknown format ${id}`);
  return info;
}
