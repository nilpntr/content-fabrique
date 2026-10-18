import { sampleFacets } from "@/lib/sample-facets";
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

const clip = (n: number, title: string, seconds: number, labels: string[], hue: number, figure?: Figure, exercise?: string): Asset => ({
  id: assetId(n),
  title,
  posterUrl: poster(hue, figure),
  durationMs: seconds * 1000,
  facets: sampleFacets(...labels),
  ...(exercise ? { exercise } : {}),
});

export const SAMPLE_ASSETS: Asset[] = [
  clip(1, "Back double biceps pose", 8, ["type:pose", "body:back", "angle:back"], 262, "standing", "back double biceps"),
  clip(2, "Lat pulldown, slow reps", 34, ["type:lift", "body:back", "movement:pull", "angle:front", "equipment:cable"], 205, "bar", "lat pulldown"),
  clip(3, "Barbell row, side angle", 27, ["type:lift", "body:back", "movement:row", "angle:side", "equipment:barbell"], 228, "bar", "barbell row"),
  clip(4, "Front lat spread", 6, ["type:pose", "body:back", "angle:front"], 280, "standing", "front lat spread"),
  clip(5, "Gym walk-in, wide shot", 12, ["type:b_roll", "angle:wide"], 150, "wide"),
  clip(6, "Chalk up and grip", 5, ["type:b_roll", "angle:close_up"], 40, "wide"),
  clip(7, "Deadlift lockout", 19, ["type:lift", "body:legs", "movement:hinge", "angle:side", "equipment:barbell"], 12, "bar", "deadlift"),
  clip(8, "Squat, rear view", 31, ["type:lift", "body:legs", "movement:squat", "angle:back", "equipment:barbell"], 330, "bar", "back squat"),
  clip(9, "Most muscular pose", 7, ["type:pose", "body:chest", "angle:front"], 300, "standing", "most muscular"),
  clip(10, "Cable fly, close up", 22, ["type:lift", "body:chest", "movement:fly", "angle:close_up", "equipment:cable"], 185, "bar", "cable fly"),
  clip(11, "Bench press, set of 8", 41, ["type:lift", "body:chest", "movement:press", "angle:side", "equipment:barbell"], 250, "bar", "bench press"),
  clip(12, "Pre-workout shake", 9, ["type:product", "angle:close_up"], 95, "wide"),
  clip(13, "Hammer curls, slow motion", 29, ["type:lift", "type:slow_motion", "body:arms", "movement:curl", "angle:front", "equipment:dumbbell"], 70, "bar", "hammer curl"),
  clip(14, "Mirror check, before", 11, ["type:before_after", "body:full_body", "angle:front"], 350, "standing"),
  clip(15, "Morning check-in", 13, ["type:talk", "angle:front"], 170, "standing"),
  clip(16, "Cool-down stretch", 48, ["body:legs", "movement:stretch", "angle:wide"], 120, "wide", "hamstring stretch"),
];
