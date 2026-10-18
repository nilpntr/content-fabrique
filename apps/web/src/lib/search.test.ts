import { sampleFacets } from "@/lib/sample-facets";
import { expect, test } from "vitest";
import { apiAsset, projectSummary } from "@/test-fixtures";
import { matchesQuery, searchClips, searchProjects } from "./search";

test("every word of the query has to be in the text, in any case and any order", () => {
  expect(matchesQuery("lat pull", "Heaviest LAT pulldown")).toBe(true);
  expect(matchesQuery("pull lat", "Heaviest lat pulldown")).toBe(true);
  expect(matchesQuery("lat squat", "Heaviest lat pulldown")).toBe(false);
  // Fields can be lists, and missing ones are skipped.
  expect(matchesQuery("chalk", "A title", ["back", "chalk"], undefined, null)).toBe(true);
  // An empty query matches nothing: the palette then shows its pages.
  expect(matchesQuery("   ", "anything")).toBe(false);
});

test("projects are found by title, and capped", () => {
  const projects = Array.from({ length: 8 }, (_, i) => projectSummary(i + 1, { title: i % 2 === 0 ? `Back day ${i}` : `Legs ${i}` }));
  expect(searchProjects(projects, "back").map((project) => project.title)).toEqual(["Back day 0", "Back day 2", "Back day 4", "Back day 6"]);
  expect(searchProjects(projects, "day", 2)).toHaveLength(2);
  expect(searchProjects(projects, "")).toEqual([]);
});

test("clips are found by what the quick look wrote and by file name, and images are not clips", () => {
  const assets = [
    apiAsset(1, { title: "Seated cable row", description: "Controlled rows with a pause", facets: sampleFacets("type:lift", "body:back") }),
    apiAsset(2, { title: "Clip 2", description: "", exercise: "lat pulldown" }),
    apiAsset(3, { title: "Clip 3", filename: "treadmill-warmup.mp4" }),
    apiAsset(4, { title: "Row photo", kind: "image" }),
  ];
  expect(searchClips(assets, "row").map((asset) => asset.title)).toEqual(["Seated cable row"]);
  expect(searchClips(assets, "pause").map((asset) => asset.id)).toEqual([assets[0]?.id]);
  expect(searchClips(assets, "pulldown").map((asset) => asset.title)).toEqual(["Clip 2"]);
  expect(searchClips(assets, "treadmill").map((asset) => asset.title)).toEqual(["Clip 3"]);
});
