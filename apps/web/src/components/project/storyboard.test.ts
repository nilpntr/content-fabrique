import { expect, test } from "vitest";
import { mainItems } from "@/components/project/review-data";
import { assetId, PLAN_FORMAT, PLAN_PAYLOAD, v2Spec } from "@/test-fixtures";
import { planFromArtifact, planFromSummaries } from "./run-artifacts";
import { describeShot, lengthWords, storyboard, storyboardFromItems, storyboardFromPlan, storyIdea, subjectOf, versionLabel } from "./storyboard";

const TITLES = new Map([
  [assetId(1), "Heaviest lat pulldown"],
  [assetId(2), "Seated cable row"],
  [assetId(3), "Back pose, held"],
]);

test("the plan reads as a storyboard: how it opens, the middle in the creator's words, how it ends", () => {
  const plan = planFromArtifact(PLAN_PAYLOAD);
  if (!plan) throw new Error("no plan");
  const board = storyboardFromPlan(plan, TITLES, [{ at_ms: 300, text: "BACK DAY 101" }]);
  expect(board?.intro).toBe("Gino's back day in 25 seconds: three pulls, one posing finish.");
  expect(board?.length).toBe("About 24 seconds");
  expect(board?.more).toBe(0);
  expect(board?.cards.map((card) => card.line)).toEqual([
    "Opens on your heaviest lat pulldown lockout with the title BACK DAY 101, with a quick zoom",
    "Your seated row, full rep, cut on the music",
    "Your cable row, squeeze, cut on the music, fast, then in slow motion",
    "Peaks on your pulldown, top of the rep, with a quick swipe in",
    "A closer look at your chalked hands",
    "Ends on your back pose, held, with a flash",
  ]);
  // Each card knows its clip, for the frame.
  expect(board?.cards.map((card) => card.assetId)).toEqual([assetId(1), assetId(2), assetId(3), assetId(1), assetId(2), assetId(3)]);
  expect(board?.cards.map((card) => card.place)).toEqual(["first", "middle", "middle", "middle", "middle", "last"]);
});

test("no line of the storyboard uses the editor's jargon", () => {
  const plan = planFromArtifact(PLAN_PAYLOAD);
  if (!plan) throw new Error("no plan");
  const text = storyboardFromPlan(plan, TITLES)?.cards.map((card) => card.line).join(" ") ?? "";
  expect(text).not.toMatch(/\b(BPM|beats?|hook|build|punch_in|speed_ramp|whip|playbook|shot|ast_)\b/i);
});

test("a plan known only from a cut-off summary (no shot names) gives way to the clips of the version", () => {
  const partial = planFromSummaries(undefined, "Plan saved (it is shown to the creator). Content type workout, playbook workout.v1. Tempo: 124 BPM, Shot list, 2 shots, 3.0 s in all (target 3 s): 1. hook: 3 beats = 1.45 s 2. ending: 3 beats = 1.45 s");
  expect(partial?.shots.length).toBeGreaterThan(0);
  expect(storyboardFromPlan(partial as NonNullable<typeof partial>, TITLES)).toBeUndefined();
  const items = mainItems(v2Spec());
  const board = storyboard({ plan: partial, items, assetTitles: TITLES, musical: true, durationMs: 10_900 });
  expect(board?.cards[0]?.line).toBe("Opens on your heaviest lat pulldown, with a quick zoom");
  expect(board?.cards.at(-1)?.line).toBe("Ends on your back pose, held, zooming out");
  expect(board?.length).toBe("About 11 seconds");
});

test("the clips of a version read as a storyboard too, with what is on screen", () => {
  const items = mainItems(v2Spec());
  const board = storyboardFromItems(items, TITLES, [{ at_ms: 0, text: "BACK\nDAY" }, { at_ms: 6300, text: "Squeeze" }], { musical: true });
  expect(board?.cards.map((card) => card.line)).toEqual([
    "Opens on your heaviest lat pulldown with the title BACK DAY, with a quick zoom",
    "Your seated cable row, cut on the music, with a quick swipe in",
    "Your back pose, held, cut on the music, fast, then in slow motion",
    "Your heaviest lat pulldown, cut on the music, with a shake on the hit",
    "Your seated cable row, cut on the music, slowly zooming in, with “Squeeze” on screen",
    "Ends on your back pose, held, zooming out",
  ]);
});

test("a long board shows its first moments, how many more, and the ending", () => {
  const items = Array.from({ length: 12 }, (_, i) => ({ id: `c${i + 1}`, asset_id: assetId(1 + (i % 3)), at_ms: i * 1000, source: { in_ms: 0, out_ms: 1000 }, speed: 1 }));
  const board = storyboardFromItems(items, TITLES);
  expect(board?.cards).toHaveLength(7);
  expect(board?.more).toBe(5);
  expect(board?.cards.at(-1)?.place).toBe("last");
  expect(storyboardFromItems([], TITLES)).toBeUndefined();
});

test("the small words: whose clip it is, and how long the video is", () => {
  expect(subjectOf("Heaviest lat pulldown.")).toBe("your heaviest lat pulldown");
  expect(subjectOf("The walk to the rack")).toBe("the walk to the rack");
  expect(subjectOf("PR attempt")).toBe("your PR attempt");
  expect(describeShot({ place: "middle", role: "build", what: "Rows from behind", musical: true })).toBe("Your rows from behind, cut on the music");
  expect(describeShot({ place: "middle", role: "build", what: "Rows from behind", musical: false, effect: "something_new" })).toBe("Your rows from behind");
  expect(lengthWords(undefined)).toBeUndefined();
  expect(lengthWords(500)).toBeUndefined();
  expect(lengthWords(24_200)).toBe("About 24 seconds");
  expect(lengthWords(65_000)).toBe("About 1 minute");
});

test("the plan's format is the storyboard's idea, and names a new idea's version", () => {
  const plan = planFromArtifact({ ...PLAN_PAYLOAD, format: PLAN_FORMAT });
  expect(plan?.idea).toEqual({ id: "transformation", name: "Transformation reveal", why: PLAN_FORMAT.why, mixName: "Seamless loop", newToCreator: true, newIdea: true });
  expect(storyIdea(plan)).toEqual({ name: "Transformation reveal", why: PLAN_FORMAT.why, mix: "with a seamless loop", isNew: true });
  expect(plan ? storyboardFromPlan(plan, TITLES)?.idea?.name : undefined).toBe("Transformation reveal");
  expect(versionLabel("Version 3", plan, true)).toBe("Version 3 · new idea: transformation reveal");
  expect(versionLabel("Version 3", plan, false)).toBe("Version 3");
  // A plan from before formats, or a format that cannot be read, has no idea.
  const old = planFromArtifact(PLAN_PAYLOAD);
  expect(old?.idea).toBeUndefined();
  expect(storyIdea(old)).toBeUndefined();
  expect(versionLabel("Version 2", old, true)).toBe("Version 2");
  expect(planFromArtifact({ ...PLAN_PAYLOAD, format: { id: "pov" } })?.idea).toBeUndefined();
  expect(planFromArtifact({ ...PLAN_PAYLOAD, format: "pov" })?.idea).toBeUndefined();
  // An acronym in a name keeps its capitals.
  expect(versionLabel("Version 4", planFromArtifact({ ...PLAN_PAYLOAD, format: { id: "pov", name: "POV video" } }), true)).toBe("Version 4 · new idea: POV video");
});
