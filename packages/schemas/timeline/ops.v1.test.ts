// Schema tests for the edit ops v1 (P3-02, design note docs/design/timeline-v1.md section 4).
//
// Every file in fixtures/ops/valid is one op that passes the schema; every file in fixtures/ops/invalid is one that
// does not. The Go package apps/server/internal/timeline/ops reads the same fixtures and must agree.
import { readdirSync, readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import Ajv2020 from "ajv/dist/2020";
import { describe, expect, it } from "vitest";

const dir = dirname(fileURLToPath(import.meta.url));
const fixtures = join(dir, "fixtures", "ops");

const OPS = [
  "add_clip",
  "remove_item",
  "move_item",
  "trim_item",
  "replace_source",
  "set_speed",
  "set_reframe",
  "add_text",
  "update_text",
  "remove_text",
  "set_transition",
  "set_sound",
  "remove_sound",
  "snap_cuts_to_beats",
  "set_look",
  "set_captions",
  "set_clip_audio",
  "set_motion",
  "set_speed_ramp",
];

type Json = Record<string, any>;
const readJson = (path: string): Json => JSON.parse(readFileSync(path, "utf8"));
const list = (sub: string) =>
  readdirSync(join(fixtures, sub))
    .filter((f) => f.endsWith(".json"))
    .sort();

const schema = readJson(join(dir, "ops.v1.schema.json"));
const timelineSchema = readJson(join(dir, "v1.schema.json"));
// Strict mode on, except strictTuples (as for the timeline schema).
const ajv = new Ajv2020({ strict: true, strictTuples: false, allErrors: true });
const validate = ajv.compile(schema);
const validateBatch = ajv.compile({ $ref: `${schema.$id}#/$defs/opBatch` });

const validFiles = list("valid");
const invalidFiles = list("invalid");

describe("op schema", () => {
  it("has one definition per op, in the root oneOf, with a title and a description", () => {
    expect(schema.oneOf.map((r: Json) => r.$ref)).toEqual(OPS.map((n) => `#/$defs/${n}`));
    for (const name of OPS) {
      const def = schema.$defs[name];
      expect(def.title, name).toBe(name);
      expect(def.description, name).toBeTruthy();
    }
    expect(schema.title).toBe("EditOp");
    expect(schema.description).toBeTruthy();
  });

  it("documents every property of every op", () => {
    const missing: string[] = [];
    const walk = (node: Json, path: string) => {
      if (node.properties) {
        for (const [k, v] of Object.entries<Json>(node.properties)) {
          if (!v.description && !v.$ref) missing.push(`${path}/${k}`);
          if (v.properties) walk(v, `${path}/${k}`);
        }
      }
    };
    // The oneOf/anyOf/not keywords of an op only repeat property names (as `{}`) to express "exactly one of"; set_sound
    // is the exception, whose three variants are full objects.
    for (const name of OPS) {
      if (name === "set_sound") for (const v of schema.$defs[name].oneOf) walk(v, name);
      else walk(schema.$defs[name], name);
    }
    expect(missing).toEqual([]);
  });

  it("keeps the definitions it shares with the timeline schema equal to theirs", () => {
    const shared = [
      "ms",
      "unit",
      "gainDb",
      "itemId",
      "styleId",
      "assetId",
      "momentId",
      "trendId",
      "licenseId",
      "reframe",
      "motion",
      "speedRamp",
      "look",
      "transition",
      "videoAudio",
      "audioDuration",
      "fadeIn",
      "fadeOut",
    ];
    for (const name of shared) expect(schema.$defs[name], name).toEqual(timelineSchema.$defs[name]);
    const item = timelineSchema.$defs.videoItem.properties;
    expect(schema.$defs.speed).toEqual(item.speed);
    expect(schema.$defs.sourceRange).toEqual(item.source);
    expect(schema.$defs.why).toEqual(item.why);
    const text = timelineSchema.$defs.textItem.properties;
    expect(schema.$defs.textBody).toEqual(text.text);
    expect(schema.$defs.textDuration).toEqual(text.duration_ms);
    expect(schema.$defs.textAnchor).toEqual(text.anchor);
    expect(schema.$defs.textPosition).toEqual(text.position);
    expect(schema.$defs.textAnimation).toEqual(text.animation);
  });

  it("is closed: no op takes a property it does not list", () => {
    for (const name of OPS) {
      const variants = name === "set_sound" ? schema.$defs[name].oneOf : [schema.$defs[name]];
      for (const v of variants) expect(v.additionalProperties, name).toBe(false);
    }
  });
});

describe("valid op fixtures", () => {
  it("has a fixture for every op", () => {
    expect(validFiles.length).toBeGreaterThanOrEqual(30);
    const seen = new Set(validFiles.map((f) => readJson(join(fixtures, "valid", f)).op));
    expect([...seen].sort()).toEqual([...OPS].sort());
  });

  it.each(validFiles)("%s passes the schema", (f) => {
    const op = readJson(join(fixtures, "valid", f));
    const ok = validate(op);
    expect(validate.errors ?? []).toEqual([]);
    expect(ok).toBe(true);
    expect(validateBatch([op, op])).toBe(true);
  });
});

describe("invalid op fixtures", () => {
  it("has at least 30", () => {
    expect(invalidFiles.length).toBeGreaterThanOrEqual(30);
  });

  it.each(invalidFiles)("%s fails the schema", (f) => {
    const op = readJson(join(fixtures, "invalid", f));
    expect(validate(op), JSON.stringify(op)).toBe(false);
    expect(validateBatch([op])).toBe(false);
  });
});

describe("batches", () => {
  const op = { op: "remove_item", id: "c1" };
  it("accepts 1 to 100 ops", () => {
    expect(validateBatch([op])).toBe(true);
    expect(validateBatch(Array.from({ length: 100 }, () => op))).toBe(true);
  });
  it("rejects an empty batch and one of 101", () => {
    expect(validateBatch([])).toBe(false);
    expect(validateBatch(Array.from({ length: 101 }, () => op))).toBe(false);
  });
  it("rejects a batch that is not an array", () => {
    expect(validateBatch(op)).toBe(false);
  });
});
