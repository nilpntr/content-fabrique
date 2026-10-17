// Schema tests for the timeline spec v1 (P3-01, design note docs/design/timeline-v1.md section 7).
//
// Valid fixtures pass the JSON Schema. Invalid fixtures name the issue code they must raise in "$comment": the ones
// that expect `schema` must fail the JSON Schema, and the ones that expect a semantic code must pass it, because the
// Go layer (apps/server/internal/timeline) is what catches them.
import { readdirSync, readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import Ajv2020 from "ajv/dist/2020";
import { describe, expect, it } from "vitest";

const dir = dirname(fileURLToPath(import.meta.url));
const fixtures = join(dir, "fixtures");

const SEMANTIC_CODES = [
  "duplicate_id",
  "gap",
  "overlap",
  "source_out_of_range",
  "shot_too_short",
  "duration_out_of_range",
  "outside_timeline",
  "unsafe_text",
  "unknown_asset",
  "transition_too_long",
];

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

describe("fixture sets", () => {
  it("has 30 valid and 30 invalid fixtures", () => {
    expect(validFiles).toHaveLength(30);
    expect(invalidFiles).toHaveLength(30);
  });

  it("names the expected issue code in every invalid fixture, and covers every code", () => {
    const codes = new Set<string>();
    for (const f of invalidFiles) {
      const m = /^expect: (\w+)$/.exec(readJson(join(fixtures, "invalid", f)).$comment ?? "");
      expect(m, f).not.toBeNull();
      codes.add(m?.[1] ?? "");
    }
    for (const code of ["schema", ...SEMANTIC_CODES]) expect(codes, code).toContain(code);
    expect([...codes].filter((c) => c !== "schema" && !SEMANTIC_CODES.includes(c))).toEqual([]);
  });

  it("has file names that are unique and carry no $comment on valid fixtures", () => {
    for (const f of validFiles) expect(readJson(join(fixtures, "valid", f)).$comment, f).toBeUndefined();
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
    const spec = readJson(join(fixtures, "invalid", f));
    const code = /^expect: (\w+)$/.exec(spec.$comment)?.[1];
    const ok = validate(spec);
    if (code === "schema") {
      expect(ok, "expected a schema error").toBe(false);
    } else {
      // A semantic issue: structurally valid, the Go layer raises the code.
      expect(validate.errors ?? []).toEqual([]);
      expect(ok).toBe(true);
    }
  });

  it("rejects $comment below the top level", () => {
    const spec = readJson(join(fixtures, "valid", validFiles[0]));
    spec.output.$comment = "nope";
    expect(validate(spec)).toBe(false);
  });
});

describe("render fixtures", () => {
  const listed = readFileSync(join(fixtures, "render.txt"), "utf8").split("\n").filter(Boolean);
  const specs = listed.map((f) => readJson(join(fixtures, "valid", f)));
  const items = (type: string) =>
    specs.flatMap((s) => s.tracks.filter((t: Json) => t.type === type).flatMap((t: Json) => t.items ?? []));

  it("lists 20 valid fixtures", () => {
    expect(listed).toHaveLength(20);
    expect(new Set(listed).size).toBe(20);
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
