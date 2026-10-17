import type { Asset, AssetId } from "./types";

/** Sample library for the new-project flow, until it is wired to the API. No network: posters are inline SVG. */

type Figure = "standing" | "bar" | "wide";

function assetId(n: number): AssetId {
  return `ast_01jh6m${String(n).padStart(20, "0")}`;
}

const FIGURES: Record<Figure, string> = {
  standing:
    '<circle cx="80" cy="72" r="14" fill="rgba(255,255,255,.7)"/><path d="M48 150c0-26 14-46 32-46s32 20 32 46z" fill="rgba(255,255,255,.55)"/>',
  bar: '<rect x="14" y="108" width="132" height="6" rx="3" fill="rgba(255,255,255,.75)"/><circle cx="80" cy="82" r="13" fill="rgba(255,255,255,.7)"/><rect x="58" y="96" width="44" height="40" rx="14" fill="rgba(255,255,255,.55)"/>',
  wide: '<circle cx="52" cy="90" r="12" fill="rgba(255,255,255,.7)"/><rect x="32" y="104" width="86" height="34" rx="16" fill="rgba(255,255,255,.55)"/>',
};

/** A soft gradient poster with a simple figure, as a data URI. */
export function poster(hue: number, figure: Figure = "standing"): string {
  const a = `hsl(${hue} 55% 62%)`;
  const b = `hsl(${(hue + 40) % 360} 60% 32%)`;
  const svg = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 160 213"><defs><linearGradient id="g" x1="0" y1="0" x2="1" y2="1"><stop offset="0" stop-color="${a}"/><stop offset="1" stop-color="${b}"/></linearGradient></defs><rect width="160" height="213" fill="url(#g)"/>${FIGURES[figure]}</svg>`;
  return `data:image/svg+xml;utf8,${encodeURIComponent(svg)}`;
}

const clip = (n: number, title: string, seconds: number, tags: string[], hue: number, figure?: Figure): Asset => ({
  id: assetId(n),
  title,
  posterUrl: poster(hue, figure),
  durationMs: seconds * 1000,
  tags,
});

export const SAMPLE_ASSETS: Asset[] = [
  clip(1, "Back double biceps pose", 8, ["posing", "back"], 262, "standing"),
  clip(2, "Lat pulldown, slow reps", 34, ["exercise", "back"], 205, "bar"),
  clip(3, "Barbell row, side angle", 27, ["exercise", "back"], 228, "bar"),
  clip(4, "Front lat spread", 6, ["posing", "back"], 280, "standing"),
  clip(5, "Gym walk-in, wide shot", 12, ["b-roll", "gym"], 150, "wide"),
  clip(6, "Chalk up and grip", 5, ["b-roll", "gym"], 40, "wide"),
  clip(7, "Deadlift lockout", 19, ["exercise", "legs"], 12, "bar"),
  clip(8, "Squat, rear view", 31, ["exercise", "legs"], 330, "bar"),
  clip(9, "Most muscular pose", 7, ["posing", "chest"], 300, "standing"),
  clip(10, "Cable fly, close up", 22, ["exercise", "chest"], 185, "bar"),
  clip(11, "Bench press, set of 8", 41, ["exercise", "chest"], 250, "bar"),
  clip(12, "Pre-workout shake", 9, ["lifestyle", "nutrition"], 95, "wide"),
  clip(13, "Meal prep timelapse", 64, ["lifestyle", "nutrition"], 70, "wide"),
  clip(14, "Mirror check, before", 11, ["progress", "posing"], 350, "standing"),
  clip(15, "Mirror check, six months later", 13, ["progress", "posing"], 170, "standing"),
  clip(16, "Cool-down stretch", 48, ["lifestyle", "legs"], 120, "wide"),
];
