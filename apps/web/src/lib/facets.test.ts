import { expect, test } from "vitest";
import { cardBadges, clipHeading, countFacets, hasSelection, matchesFacets, optionsFromSummaries } from "./facets";
import { sampleFacets } from "./sample-facets";

const clips = [
  { facets: sampleFacets("type:lift", "body:back", "movement:row", "angle:side", "equipment:cable") },
  { facets: sampleFacets("type:lift", "body:back", "movement:pull", "angle:back") },
  { facets: sampleFacets("type:pose", "body:back", "angle:back") },
  { facets: sampleFacets("type:lift", "type:slow_motion", "body:arms", "angle:front") },
  { facets: [] },
];

test("the filters count the values the clips have, most first, and leave each filter's own choice out of its counts", () => {
  expect(countFacets(clips, {})).toEqual({
    body: [
      { value: "back", label: "Back", count: 3 },
      { value: "arms", label: "Arms", count: 1 },
    ],
    type: [
      { value: "lift", label: "Lifting", count: 3 },
      { value: "pose", label: "Posing", count: 1 },
      { value: "slow_motion", label: "Slow motion", count: 1 },
    ],
    angle: [
      { value: "back", label: "Back view", count: 2 },
      { value: "front", label: "Front view", count: 1 },
      { value: "side", label: "Side view", count: 1 },
    ],
  });
  const back = countFacets(clips, { body: "back" });
  expect(back.body?.map((o) => o.value)).toEqual(["back", "arms"]); // still switchable
  expect(back.type).toEqual([
    { value: "lift", label: "Lifting", count: 2 },
    { value: "pose", label: "Posing", count: 1 },
  ]);
  expect(countFacets([], {})).toEqual({});
});

test("a clip matches when it has every picked label", () => {
  const [row, , pose] = clips;
  expect(matchesFacets(row?.facets ?? [], { body: "back", angle: "side" })).toBe(true);
  expect(matchesFacets(pose?.facets ?? [], { body: "back", angle: "side" })).toBe(false);
  expect(matchesFacets(pose?.facets ?? [], { body: "back", angle: "side" }, "angle")).toBe(true);
  expect(matchesFacets([], {})).toBe(true);
  expect(hasSelection({})).toBe(false);
  expect(hasSelection({ type: "lift" })).toBe(true);
});

test("a card shows at most two labels, the telling ones", () => {
  const labels = (...refs: string[]) => cardBadges(sampleFacets(...refs)).map((f) => f.label);
  expect(labels("type:lift", "body:back", "movement:row", "angle:side", "equipment:cable")).toEqual(["Back", "Side view"]);
  expect(labels("type:pose", "body:back", "angle:back")).toEqual(["Back", "Posing"]);
  expect(labels("type:lift", "type:slow_motion", "body:arms", "angle:front")).toEqual(["Slow motion", "Arms"]);
  expect(labels("type:talk", "angle:front")).toEqual(["Talking", "Front view"]);
  expect(labels("equipment:cable")).toEqual([]);
});

test("the heading is the exercise, unless the creator named the clip", () => {
  expect(clipHeading({ title: "Seated cable row, back view", exercise: "seated cable row", titleSource: "quicklook" })).toBe("Seated cable row");
  expect(clipHeading({ title: "My best set", exercise: "seated cable row", titleSource: "user" })).toBe("My best set");
  expect(clipHeading({ title: "IMG_0042", exercise: "" })).toBe("IMG_0042");
});

test("the server's counts become the filters' options, the three facets only", () => {
  const options = optionsFromSummaries([
    { facet: "movement", values: [{ value: "row", label: "Row", count: 4 }] },
    { facet: "angle", values: [{ value: "side", label: "Side view", count: 1 }, { value: "back", label: "Back view", count: 3 }] },
  ]);
  expect(options).toEqual({ angle: [{ value: "back", label: "Back view", count: 3 }, { value: "side", label: "Side view", count: 1 }] });
  expect(optionsFromSummaries(undefined)).toEqual({});
});
