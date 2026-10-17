// Schema tests for the timeline spec v1 (P3-01, design note docs/design/timeline-v1.md section 7).
//
// Valid fixtures pass the JSON Schema. fixtures/expect.json maps every invalid fixture to the issue code it must
// raise: the ones that expect `schema` must fail the JSON Schema, at the given instance path (and keyword), and the
// ones that expect a semantic code must pass it, because the Go layer (apps/server/internal/timeline) catches them.
import { readdirSync, readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import Ajv2020 from "ajv/dist/2020";
import { describe, expect, it } from "vitest";

const dir = dirname(fileURLToPath(import.meta.url));
const fixtures = join(dir, "fixtures");

interface Expect {
  code: string;
  path?: string;
  keyword?: string;
}

type Json = Record<string, any>;
const readJson = (path: string): Json => JSON.parse(readFileSync(path, "utf8"));
const list = (sub: string) =>
  readdirSync(join(fixtures, sub))
    .filter((f) => f.endsWith(".json"))
    .sort();

const schema = readJson(join(dir, "v1.schema.json"));
// Strict mode on, except strictTuples: the first-clip-is-a-cut rule is a one-element prefixItems on an open-ended list,
// which Ajv would only accept as a full tuple.
const ajv = new Ajv2020({ strict: true, strictTuples: false, allErrors: true });
const validate = ajv.compile(schema);

const validFiles = list("valid");
const invalidFiles = list("invalid");

const expectations = readJson(join(fixtures, "expect.json")) as Record<string, Expect>;

describe("fixture sets", () => {
  it("has at least 30 valid and 30 invalid fixtures", () => {
    expect(validFiles.length).toBeGreaterThanOrEqual(30);
    expect(invalidFiles.length).toBeGreaterThanOrEqual(30);
  });

  it("has an expectation for every invalid fixture and no others, with an issue code each", () => {
    expect(Object.keys(expectations).sort()).toEqual(invalidFiles);
    // The codes are whatever expect.json names (a new code needs a fixture and an entry, not an edit here): schema, and the
    // semantic codes the Go layer raises, which are lower_snake_case words.
    const codes = new Set(Object.values(expectations).map((e) => e.code));
    expect(codes).toContain("schema");
    for (const code of codes) expect(code, code).toMatch(/^[a-z]+(_[a-z]+)*$/);
    expect(codes.size).toBeGreaterThanOrEqual(15);
  });

  it("gives every schema expectation an instance path, and no other expectation one", () => {
    for (const [f, e] of Object.entries(expectations)) {
      if (e.code === "schema") expect(e.path, f).toMatch(/^(\/[^/]+)*$/);
      else expect(e.path ?? e.keyword, f).toBeUndefined();
    }
  });

  it("keeps the production schema closed and the fixtures free of $comment", () => {
    expect(schema.properties).not.toHaveProperty("$comment");
    for (const f of validFiles) expect(readJson(join(fixtures, "valid", f)).$comment, f).toBeUndefined();
    for (const f of invalidFiles) expect(readJson(join(fixtures, "invalid", f)).$comment, f).toBeUndefined();
  });
});

describe("valid fixtures", () => {
  it.each(validFiles)("%s passes the schema", (f) => {
    const ok = validate(readJson(join(fixtures, "valid", f)));
    expect(validate.errors ?? []).toEqual([]);
    expect(ok).toBe(true);
  });
});

describe("invalid fixtures", () => {
  it.each(invalidFiles)("%s", (f) => {
    const want = expectations[f];
    const ok = validate(readJson(join(fixtures, "invalid", f)));
    if (want.code === "schema") {
      expect(ok, "expected a schema error").toBe(false);
      const errors = validate.errors ?? [];
      const hit = errors.some((e) => e.instancePath === want.path && (want.keyword === undefined || e.keyword === want.keyword));
      expect(hit, `want ${want.keyword ?? "any error"} at ${want.path}, got ${errors.map((e) => `${e.instancePath} ${e.keyword}`).join(", ")}`).toBe(true);
    } else {
      // A semantic issue: structurally valid, the Go layer raises the code.
      expect(validate.errors ?? []).toEqual([]);
      expect(ok).toBe(true);
    }
  });

  it("rejects $comment at every level", () => {
    const top = readJson(join(fixtures, "valid", validFiles[0]));
    top.$comment = "nope";
    expect(validate(top)).toBe(false);
    const nested = readJson(join(fixtures, "valid", validFiles[0]));
    nested.output.$comment = "nope";
    expect(validate(nested)).toBe(false);
  });
});

describe("render fixtures", () => {
  // render.txt is the P3-03 list; render-text.txt adds the text and caption fixtures (P3-04). More lists may follow.
  const listed = readdirSync(fixtures)
    .filter((f) => /^render(-[a-z]+)?\.txt$/.test(f))
    .flatMap((f) => readFileSync(join(fixtures, f), "utf8").split("\n").filter(Boolean));
  // The coverage checks below are the P3-03 ones, on render.txt; later lists add their own fixtures (text, captions) and are
  // held to the first test only.
  const baseList = readFileSync(join(fixtures, "render.txt"), "utf8").split("\n").filter(Boolean);
  const specs = baseList.map((f) => readJson(join(fixtures, "valid", f)));
  const items = (type: string) =>
    specs.flatMap((s) => s.tracks.filter((t: Json) => t.type === type).flatMap((t: Json) => t.items ?? []));

  it("lists at least 20 valid fixtures, each once", () => {
    expect(listed.length).toBeGreaterThanOrEqual(20);
    expect(new Set(listed).size).toBe(listed.length);
    for (const f of listed) expect(validFiles, f).toContain(f);
  });

  it("covers every format, transition, reframe mode, look, speed range and audio kind", () => {
    const video = items("video");
    expect(new Set(specs.map((s) => s.output.format))).toEqual(new Set(["9:16", "1:1", "4:5", "16:9"]));
    expect(new Set(video.map((c: Json) => c.transition_in?.type).filter(Boolean))).toEqual(
      new Set(["cut", "crossfade", "dip_to_black", "whip"]),
    );
    expect(new Set(video.map((c: Json) => c.reframe?.mode).filter(Boolean))).toEqual(
      new Set(["cover", "contain", "blur_fill"]),
    );
    expect(new Set(video.map((c: Json) => c.look?.preset).filter(Boolean))).toEqual(
      new Set(["none", "punchy", "warm", "cool", "bw", "matte"]),
    );
    const speeds = video.map((c: Json) => c.speed ?? 1);
    expect(Math.min(...speeds)).toBe(0.25);
    expect(Math.max(...speeds)).toBe(4);
    expect(speeds.some((x: number) => x < 1)).toBe(true);
    expect(speeds.some((x: number) => x > 1)).toBe(true);
    expect(new Set(items("audio").map((a: Json) => a.kind))).toEqual(
      new Set(["trend_sound", "licensed_track", "creator_audio"]),
    );
    expect(new Set(items("text").map((t: Json) => t.anchor).filter(Boolean))).toEqual(
      new Set(["upper_third", "center", "lower_third"]),
    );
    expect(items("text").some((t: Json) => t.position)).toBe(false);
    expect(specs.some((s) => s.tracks.some((t: Json) => t.type === "captions"))).toBe(true);
  });
});

describe("motion fixtures (P3-10)", () => {
  // render-motion.txt: one fixture per format, each with every motion preset, a speed ramp and the new transitions.
  const listed = readFileSync(join(fixtures, "render-motion.txt"), "utf8").split("\n").filter(Boolean);
  const specs = listed.map((f) => readJson(join(fixtures, "valid", f)));
  const video = specs.flatMap((s) => s.tracks.find((t: Json) => t.type === "video").items as Json[]);

  it("covers every format, motion preset, new transition and a speed ramp", () => {
    expect(new Set(specs.map((s) => s.output.format))).toEqual(new Set(["9:16", "1:1", "4:5", "16:9"]));
    expect(new Set(video.map((c) => c.motion?.preset).filter(Boolean))).toEqual(
      new Set(["punch_in", "push_in", "pull_back", "shake"]),
    );
    expect(new Set(video.map((c) => c.transition_in?.type).filter(Boolean))).toEqual(
      new Set(["whip_left", "whip_right", "zoom_through", "flash"]),
    );
    expect(video.some((c) => Array.isArray(c.speed_ramp))).toBe(true);
    for (const s of specs) {
      const items = s.tracks.find((t: Json) => t.type === "video").items as Json[];
      expect(new Set(items.map((c) => c.motion?.preset).filter(Boolean)).size, s.output.format).toBe(4);
      expect(items.some((c) => c.speed_ramp), s.output.format).toBe(true);
    }
  });
});

describe("look and sound effect fixtures (P3-10 part B)", () => {
  const listed = readFileSync(join(fixtures, "render-looks.txt"), "utf8").split("\n").filter(Boolean);
  const specs = listed.map((f) => readJson(join(fixtures, "valid", f)));

  it("covers every format, the new looks, match with a hint and every sound effect", () => {
    expect(new Set(specs.map((s) => s.output.format))).toEqual(new Set(["9:16", "1:1", "4:5", "16:9"]));
    const video = specs.flatMap((s) => s.tracks.find((t: Json) => t.type === "video").items as Json[]);
    const presets = new Set(video.map((c) => c.look?.preset));
    expect(presets.has("auto") && presets.has("gym_moody")).toBe(true);
    expect(video.some((c) => c.look?.match === true && typeof c.exposure_ev === "number")).toBe(true);
    for (const c of video) if (c.exposure_ev !== undefined) expect(Math.abs(c.exposure_ev)).toBeLessThanOrEqual(1);
    for (const s of specs) {
      const sounds = s.tracks.find((t: Json) => t.type === "audio").items as Json[];
      expect(new Set(sounds.map((a) => a.sfx)), s.output.format).toEqual(new Set(["whoosh", "impact", "riser", "swish"]));
      for (const a of sounds) expect(a.export).toBe("licensed");
    }
  });
});

describe("the schema", () => {
  const base = () => structuredClone(readJson(join(fixtures, "valid", "v30_full_edit.json")));
  const main = (s: Json) => s.tracks.find((t: Json) => t.type === "video");

  it("accepts the TypeID alphabet and rejects i, l, o, u and a first character above 7", () => {
    const s = base();
    const id = main(s).items[0].asset_id as string;
    for (const bad of [`ast_8${id.slice(5)}`, `ast_0${"i".repeat(25)}`, `ast_0${"l".repeat(25)}`, `ast_0${"o".repeat(25)}`, `ast_0${"u".repeat(25)}`, id.toUpperCase(), id.slice(0, -1)]) {
      const t = base();
      main(t).items[0].asset_id = bad;
      expect(validate(t), bad).toBe(false);
    }
    expect(validate(s)).toBe(true);
  });

  it("limits text to 120 characters and 3 lines", () => {
    const s = base();
    const t = s.tracks.find((x: Json) => x.type === "text");
    t.items[0].text = "x".repeat(121);
    expect(validate(s)).toBe(false);
    t.items[0].text = "a\nb\nc";
    expect(validate(s)).toBe(true);
    t.items[0].text = "a\nb\nc\nd";
    expect(validate(s)).toBe(false);
    t.items[0].text = "";
    expect(validate(s)).toBe(false);
  });

  it("allows no control characters in text except the newline, and any other character", () => {
    const s = base();
    const t = s.tracks.find((x: Json) => x.type === "text");
    for (const bad of ["a\rb", "a\tb", "a\u0000b", "a\u0085b", "a\u2028b", "a\u2029b", "a\u001bb", "a\u007fb"]) {
      t.items[0].text = bad;
      expect(validate(s), JSON.stringify(bad)).toBe(false);
    }
    for (const good of ["Ça va? 💪 日本語", "{\\an8}tags are text here", "a\n\nc"]) {
      t.items[0].text = good;
      expect(validate(s), JSON.stringify(good)).toBe(true);
    }
  });

  it("caps every time at 24 hours", () => {
    for (const [d, ok] of [[86400000, true], [86400001, false]] as const) {
      const s = base();
      main(s).items[0].source.out_ms = d;
      expect(validate(s), String(d)).toBe(ok);
      const t = base();
      t.tracks.find((x: Json) => x.type === "text").items[0].duration_ms = d;
      expect(validate(t), `text ${d}`).toBe(ok);
      const a = base();
      a.tracks.find((x: Json) => x.type === "audio").items[0].source_offset_ms = d;
      expect(validate(a), `audio ${d}`).toBe(ok);
    }
  });

  it("allows at most one track per type", () => {
    for (const type of ["text", "captions", "audio"]) {
      const s = base();
      const t = s.tracks.find((x: Json) => x.type === type);
      s.tracks.push({ ...structuredClone(t), id: `${t.id}_2` });
      expect(validate(s), type).toBe(false);
    }
    const s = base();
    s.tracks = s.tracks.filter((t: Json) => t.type !== "video");
    expect(validate(s)).toBe(false);
  });

  it("bounds transition durations to 100-1000 ms and gives a cut no duration", () => {
    for (const [d, ok] of [[99, false], [100, true], [1000, true], [1001, false]] as const) {
      const s = base();
      main(s).items[1].transition_in = { type: "crossfade", duration_ms: d };
      expect(validate(s), String(d)).toBe(ok);
    }
    const s = base();
    main(s).items[1].transition_in = { type: "cut", duration_ms: 300 };
    expect(validate(s)).toBe(false);
    main(s).items[1].transition_in = { type: "crossfade" };
    expect(validate(s)).toBe(false);
  });

  it("bounds speed, zoom, gain and the why note", () => {
    const cases: [(c: Json) => void, boolean][] = [
      [(c) => { c.speed = 0.24; }, false],
      [(c) => { c.speed = 0.25; }, true],
      [(c) => { c.speed = 4.01; }, false],
      [(c) => { c.reframe = { mode: "cover", zoom: 0.99 }; }, false],
      [(c) => { c.reframe = { mode: "cover", zoom: 3.01 }; }, false],
      [(c) => { c.reframe = { mode: "cover", focus: { x: 1.01, y: 0 } }; }, false],
      [(c) => { c.audio = { gain_db: -30.5 }; }, false],
      [(c) => { c.audio = { gain_db: 6.5 }; }, false],
      [(c) => { c.why = "w".repeat(281); }, false],
      [(c) => { c.why = "w".repeat(280); }, true],
    ];
    for (const [mutate, ok] of cases) {
      const s = base();
      mutate(main(s).items[1]);
      expect(validate(s), mutate.toString()).toBe(ok);
    }
  });

  it("requires export cue_only on a trend_sound, with a message that points at it", () => {
    const s = base();
    const audio = s.tracks.find((t: Json) => t.type === "audio");
    audio.items[0].export = "licensed";
    expect(validate(s)).toBe(false);
    expect(validate.errors?.some((e) => e.instancePath.endsWith("/export"))).toBe(true);
  });
});
