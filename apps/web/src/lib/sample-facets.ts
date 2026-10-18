import type { Facet } from "./facets";

/** The words of the server's vocabulary (config/tags/v1.yaml), for sample clips and tests only: the app itself shows the labels the API sends. */
const LABELS: Readonly<Record<string, string>> = {
  "type:lift": "Lifting",
  "type:pose": "Posing",
  "type:talk": "Talking",
  "type:b_roll": "B-roll",
  "type:product": "Product",
  "type:before_after": "Before and after",
  "type:slow_motion": "Slow motion",
  "body:back": "Back",
  "body:chest": "Chest",
  "body:shoulders": "Shoulders",
  "body:arms": "Arms",
  "body:legs": "Legs",
  "body:glutes": "Glutes",
  "body:core": "Core",
  "body:full_body": "Full body",
  "movement:pull": "Pull",
  "movement:row": "Row",
  "movement:press": "Press",
  "movement:fly": "Fly",
  "movement:squat": "Squat",
  "movement:hinge": "Hinge",
  "movement:curl": "Curl",
  "movement:extension": "Extension",
  "movement:raise": "Raise",
  "movement:cardio": "Cardio",
  "movement:stretch": "Stretch",
  "angle:front": "Front view",
  "angle:side": "Side view",
  "angle:back": "Back view",
  "angle:close_up": "Close-up",
  "angle:wide": "Wide shot",
  "equipment:machine": "Machine",
  "equipment:cable": "Cable",
  "equipment:dumbbell": "Dumbbell",
  "equipment:barbell": "Barbell",
  "equipment:kettlebell": "Kettlebell",
  "equipment:band": "Band",
  "equipment:bodyweight": "Bodyweight",
};

/** Labels from "facet:value" strings, as the API would send them. */
export function sampleFacets(...refs: string[]): Facet[] {
  return refs.map((ref) => {
    const [facet = "", value = ""] = ref.split(":");
    return { facet, value, label: LABELS[ref] ?? value };
  });
}
