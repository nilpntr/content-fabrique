import { expect, test } from "vitest";
import { atLimit, cleanCorrection, correctionsOf, MAX_CORRECTIONS, sameCorrections, setCaptionsOp, withCorrection } from "./caption-corrections";

const A = "ast_01j3er3kwcgrasdgd4aznmxxe0";
const B = "ast_01jex6xx15ayjx8epfhfjj9s11";

test("a typed fix is cleaned: white space collapses and the ends are trimmed", () => {
  expect(cleanCorrection("  Cre   atine \n")).toEqual({ ok: true, text: "Cre atine" });
  expect(cleanCorrection("Zoë's")).toEqual({ ok: true, text: "Zoë's" });
});

test("a fix the server would refuse is explained before the request", () => {
  for (const hostile of ["", "   ", "{\\an8}x", "<b>x</b>", "a|b", "a`b", "$x", "a\u0000b", "exe\u202efdp", "a\u200bb", "x".repeat(41)]) {
    const result = cleanCorrection(hostile);
    expect(result.ok, JSON.stringify(hostile)).toBe(false);
  }
});

test("a fix replaces the one before it, and a text equal to the transcript's word takes it away", () => {
  let list = withCorrection([], A, 4, "creatine", "creating");
  list = withCorrection(list, B, 1, "Whey", "way");
  list = withCorrection(list, A, 4, "Creatine", "creating");
  expect(list).toEqual([
    { asset_id: A, word: 4, text: "Creatine", was: "creating" },
    { asset_id: B, word: 1, text: "Whey", was: "way" },
  ]);
  expect(withCorrection(list, A, 4, "creating", "creating")).toEqual([{ asset_id: B, word: 1, text: "Whey", was: "way" }]);
  expect(withCorrection(list, B, 1, null, "way")).toEqual([{ asset_id: A, word: 4, text: "Creatine", was: "creating" }]);
  expect(correctionsOf(list, A).get(4)).toBe("Creatine");
  expect(correctionsOf(list, B).has(4)).toBe(false);
});

test("the list stops at the most the schema holds", () => {
  const full = Array.from({ length: MAX_CORRECTIONS }, (_, word) => ({ asset_id: A, word, text: "x" }));
  expect(withCorrection(full, A, 999, "y", "z")).toEqual(full);
  expect(withCorrection(full, A, 3, "y", "z")).toHaveLength(MAX_CORRECTIONS);
});

test("the op replaces the whole list", () => {
  expect(setCaptionsOp([{ asset_id: A, word: 4, text: "creatine" }])).toEqual({ op: "set_captions", corrections: [{ asset_id: A, word: 4, text: "creatine" }] });
  expect(setCaptionsOp([])).toEqual({ op: "set_captions", corrections: [] });
});

test("punctuation at the ends is part of a word and is kept", () => {
  for (const word of ["Dr.", "yes,", "...", "-ok-", "(ok)"]) expect(cleanCorrection(word)).toEqual({ ok: true, text: word });
  const decomposed = "Cafe\u0301";
  expect(cleanCorrection(decomposed)).toEqual({ ok: true, text: "Caf\u00e9" });
  for (const hostile of ["a" + String.fromCodePoint(0xe000), "a" + String.fromCodePoint(0xfffd), "a\ud800"]) expect(cleanCorrection(hostile).ok, hostile).toBe(false);
});

test("a save that changes nothing is recognised, and so is the full list", () => {
  const full = Array.from({ length: MAX_CORRECTIONS }, (_, word) => ({ asset_id: A, word, text: "x", was: "y" }));
  expect(sameCorrections(withCorrection(full, A, 999, "q", "z"), full)).toBe(true);
  expect(atLimit(full, A, 999)).toBe(true);
  expect(atLimit(full, A, 3)).toBe(false);
  expect(sameCorrections(withCorrection([], A, 1, "same", "same"), [])).toBe(true);
});
