import { expect, test } from "vitest";
import { pickReferences } from "@/components/style/reference-videos";
import { emptyStyle, learntStyle, styleReference } from "@/test-fixtures";
import { chosenCount, colourName, templateFromWords, templateWords, validTemplate, failureWords, learningLine, referenceLimitsLine, referencesLine, referenceStatus, SETTINGS, sourceNote, VIBES, withMySettings, withSetting } from "./style-words";

test("every setting has plain options and a line that says what it does", () => {
  for (const setting of SETTINGS) {
    expect(setting.explain.length).toBeGreaterThan(10);
    expect(setting.options.length).toBeGreaterThanOrEqual(2);
    for (const option of setting.options) expect(option.label).not.toMatch(/_/);
  }
  expect(VIBES.map((v) => v.label)).toEqual(["Calm", "Lively", "Hype"]);
});

test("where a setting came from, in words; the creator's own needs no note", () => {
  expect(sourceNote("you")).toBeNull();
  expect(sourceNote("suggested")).toBe("Suggested from your videos");
  expect(sourceNote("measured")).toBe("From your videos");
  expect(sourceNote("vibe")).toBe("From your vibe");
  expect(sourceNote("default")).toBe("Default");
});

test("a setting is set, changed and given back to the suggestion", () => {
  const one = withSetting({}, "transitions", "lots");
  expect(one).toEqual({ transitions: "lots" });
  expect(withSetting(one, "vibe", "hype")).toEqual({ transitions: "lots", vibe: "hype" });
  expect(withSetting(one, "transitions", null)).toEqual({});
  expect(chosenCount({ transitions: "lots", vibe: "hype" })).toBe(2);
});

test("the controls move at once: a chosen setting shows as the creator's", () => {
  const style = emptyStyle();
  const next = withMySettings(style, { transitions: "off", title_colour: "#FF0000" });
  expect(next.effective.transitions).toBe("off");
  expect(next.effective.sources.transitions).toBe("you");
  expect(next.effective.title_colour).toBe("#FF0000");
  expect(next.effective.camera_motion).toBe("subtle");
  expect(next.effective.sources.camera_motion).toBe("vibe");
  expect(style.effective.transitions).toBe("subtle"); // the old one is not changed
});

test("the references in words", () => {
  expect(referencesLine(emptyStyle())).toBe("Add 3 to 10 of your own finished videos.");
  expect(referencesLine(emptyStyle({ references: [styleReference(1)] }))).toBe("1 video. Add 2 more for the best result.");
  expect(referencesLine(learntStyle())).toBe("5 videos. You can add 5 more.");
  const full = emptyStyle({ references: Array.from({ length: 10 }, (_, i) => styleReference(i)) });
  expect(referencesLine(full)).toBe("10 videos. That's the most: remove one to add another.");
  expect(referenceLimitsLine(emptyStyle())).toBe("Videos up to 3 minutes and 1 GB each.");
  expect(referenceStatus(styleReference(1, { status: "processing", progress: 40 }))).toBe("Getting it ready… 40 %");
  expect(referenceStatus(styleReference(1, { status: "analysing" }))).toBe("Learning from it…");
  expect(referenceStatus(styleReference(1, { status: "failed", failure_reason: "too_long" }))).toBe("Too long: use a video of 3 minutes or less");
  expect(failureWords("something_new")).toBe("Couldn't learn from this one");
  expect(learningLine(emptyStyle({ status: "learning", progress: { done: 2, total: 5 } }))).toBe("Learning your style… 2 of 5 videos done");
  expect(learningLine(emptyStyle({ status: "learning", progress: { done: 5, total: 5 } }))).toBe("Writing your style…");
  expect(learningLine(learntStyle())).toBeNull();
  expect(colourName("#fff4d6")).toBe("Cream");
  expect(colourName("#123456")).toBe("#123456");
});

test("only videos that fit become style videos", () => {
  const file = (name: string, type: string) => new File([new Uint8Array(1)], name, { type });
  const big = Object.defineProperty(file("big.mp4", "video/mp4"), "size", { value: 2 * 2 ** 30 });
  const { accepted, rejected } = pickReferences([file("a.mp4", "video/mp4"), file("b.mov", ""), file("pose.jpg", "image/jpeg"), big], emptyStyle());
  expect(accepted.map((f) => f.name)).toEqual(["a.mp4", "b.mov"]);
  expect(rejected.map((r) => r.name)).toEqual(["pose.jpg", "big.mp4"]);
  // Room for one more only.
  const nine = emptyStyle({ references: Array.from({ length: 9 }, (_, i) => styleReference(i)) });
  const two = pickReferences([file("x.mp4", "video/mp4"), file("y.mp4", "video/mp4")], nine);
  expect(two.accepted).toHaveLength(1);
  expect(two.rejected[0]?.reason).toMatch(/10 style videos at most/);
});

test("a title form is shown in small words and saved with the slots in capitals", () => {
  expect(templateWords("<N> MOVES FOR <GOAL>")).toBe("<number> MOVES FOR <goal>");
  expect(templateFromWords("  <number>  moves for <goal> ")).toBe("<N> moves for <GOAL>");
  expect(validTemplate("<topic> 7")).toBe(true);
  expect(validTemplate("<topic")).toBe(false);
  expect(validTemplate("<script>x</script>")).toBe(false);
  expect(validTemplate("")).toBe(false);
});
