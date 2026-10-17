import type { Asset, AssetId, AssetStatus, SpriteGrid } from "./types";

/**
 * Sample library until the API is wired (P1-01 / P0-08). No network and no video files: posters and sprite sheets are
 * inline SVG, so the grid, the status states and the scrub preview all work offline. The play-on-hover video can be tried
 * with your own files through `?samples=local` (see `local-samples.tsx`).
 */

function assetId(n: number): AssetId {
  return `ast_01jh7q${String(n).padStart(20, "0")}`;
}

const encode = (svg: string) => `data:image/svg+xml;utf8,${encodeURIComponent(svg)}`;

/** A soft gradient poster in the clip's own shape, with a simple figure. */
export function samplePoster(hue: number, width: number, height: number): string {
  const w = 240;
  const h = Math.round((w * height) / width);
  const a = `hsl(${hue} 55% 62%)`;
  const b = `hsl(${(hue + 40) % 360} 60% 30%)`;
  const cx = w / 2;
  const cy = h * 0.46;
  const r = Math.min(w, h) * 0.1;
  return encode(
    `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 ${w} ${h}"><defs><linearGradient id="g" x1="0" y1="0" x2="1" y2="1"><stop offset="0" stop-color="${a}"/><stop offset="1" stop-color="${b}"/></linearGradient></defs><rect width="${w}" height="${h}" fill="url(#g)"/><circle cx="${cx}" cy="${cy}" r="${r}" fill="rgba(255,255,255,.7)"/><path d="M${cx - r * 2.4} ${h}c0-${r * 3.2} ${r * 1.1}-${r * 4.6} ${r * 2.4}-${r * 4.6}s${r * 2.4} ${r * 1.4} ${r * 2.4} ${r * 4.6}z" fill="rgba(255,255,255,.5)"/></svg>`,
  );
}

/** A sprite sheet of `cols * rows` frames. The figure walks across the frame and the hue drifts, so a scrub is easy to see. */
export function sampleSprite(hue: number, grid: SpriteGrid): string {
  const { cols, rows, tileW, tileH } = grid;
  const frames = cols * rows;
  const tiles: string[] = [];
  for (let i = 0; i < frames; i++) {
    const t = i / Math.max(1, frames - 1);
    const x = (i % cols) * tileW;
    const y = Math.floor(i / cols) * tileH;
    const h1 = (hue + t * 70) % 360;
    const h2 = (hue + 40 + t * 70) % 360;
    const px = tileW * (0.2 + 0.6 * t);
    const r = Math.min(tileW, tileH) * 0.1;
    tiles.push(
      `<g transform="translate(${x} ${y})"><defs><linearGradient id="g${i}" x1="0" y1="0" x2="1" y2="1"><stop offset="0" stop-color="hsl(${h1} 55% 62%)"/><stop offset="1" stop-color="hsl(${h2} 60% 30%)"/></linearGradient></defs><rect width="${tileW}" height="${tileH}" fill="url(#g${i})"/><circle cx="${px}" cy="${tileH * 0.46}" r="${r}" fill="rgba(255,255,255,.75)"/><rect x="${px - r * 1.6}" y="${tileH * 0.46 + r * 1.3}" width="${r * 3.2}" height="${tileH * 0.3}" rx="${r}" fill="rgba(255,255,255,.5)"/></g>`,
    );
  }
  return encode(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 ${cols * tileW} ${rows * tileH}" width="${cols * tileW}" height="${rows * tileH}">${tiles.join("")}</svg>`);
}

const BASE = Date.UTC(2026, 9, 12, 9, 0, 0);

interface Spec {
  title: string;
  seconds: number;
  tags: string[];
  hue: number;
  status?: AssetStatus;
  progress?: number;
  size?: "portrait" | "landscape" | "square" | "tall";
  sprite?: boolean;
  poster?: boolean;
  description?: string;
}

const SIZES = {
  portrait: [1080, 1920],
  landscape: [1920, 1080],
  square: [1080, 1080],
  tall: [1080, 1350],
} as const;

function clip(n: number, spec: Spec): Asset {
  const [width, height] = SIZES[spec.size ?? "portrait"];
  const status = spec.status ?? "ready";
  const durationMs = spec.seconds * 1000;
  const ready = status === "ready";
  const tileW = width >= height ? 160 : Math.round((180 * width) / height);
  const tileH = width >= height ? Math.round((160 * height) / width) : 180;
  const grid: SpriteGrid = { cols: 5, rows: 4, intervalMs: Math.ceil(durationMs / 20), tileW, tileH };
  return {
    id: assetId(n),
    title: spec.title,
    description: spec.description,
    tags: spec.tags,
    status,
    progress: spec.progress,
    durationMs,
    width,
    height,
    createdAt: new Date(BASE - n * 3_600_000 * 7).toISOString(),
    posterUrl: spec.poster === false ? undefined : samplePoster(spec.hue, width, height),
    sprite: ready && spec.sprite !== false ? { url: sampleSprite(spec.hue, grid), grid } : undefined,
  };
}

export const SAMPLE_ASSETS: Asset[] = [
  clip(1, { title: "Back double biceps pose", seconds: 8, tags: ["posing", "back"], hue: 262 }),
  clip(2, { title: "Lat pulldown, slow reps", seconds: 34, tags: ["exercise", "back", "cable"], hue: 205, size: "tall" }),
  clip(3, { title: "Barbell row, side angle", seconds: 27, tags: ["exercise", "back"], hue: 228, size: "landscape" }),
  clip(4, { title: "Gym walk-in, wide shot", seconds: 12, tags: ["b-roll", "gym"], hue: 150, size: "landscape" }),
  clip(5, { title: "Chalk up and grip", seconds: 5, tags: ["b-roll"], hue: 40, size: "square" }),
  clip(6, { title: "Posing routine, full run through with music and the crowd cheering", seconds: 192, tags: ["posing", "routine", "stage", "crowd"], hue: 330 }),
  clip(7, { title: "Cable fly, front angle", seconds: 21, tags: [], hue: 18, status: "processing", progress: 42, size: "tall" }),
  clip(8, { title: "Warm-up mobility", seconds: 64, tags: [], hue: 185, status: "processing", size: "landscape" }),
  clip(9, { title: "Deadlift, heavy single", seconds: 15, tags: [], hue: 100, status: "uploading", progress: 68 }),
  clip(10, { title: "Morning check-in", seconds: 47, tags: [], hue: 290, status: "uploaded" }),
  clip(11, { title: "Corrupt export from phone", seconds: 3, tags: [], hue: 0, status: "failed", poster: false }),
  clip(12, { title: "Leg day finisher", seconds: 9, tags: ["exercise", "legs"], hue: 75, sprite: false }),
];
