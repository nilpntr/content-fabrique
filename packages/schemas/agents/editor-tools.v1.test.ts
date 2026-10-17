// Contract test of the editor agent's tools (P4-02a, docs/design/agent-runtime-v1.md section 5). The Go package
// apps/server/internal/agents/editor builds its tools from the same file and has its own contract test; this one checks that the
// schemas compile and behave under Ajv, the validator the web app and the TypeScript tooling use.
import { readdirSync, readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import Ajv2020 from "ajv/dist/2020";
import { describe, expect, it } from "vitest";

const dir = dirname(fileURLToPath(import.meta.url));
const timelineDir = join(dir, "..", "timeline");

type Json = Record<string, any>;
const readJson = (path: string): Json => JSON.parse(readFileSync(path, "utf8"));

const contract = readJson(join(dir, "editor-tools.v1.json"));
const opsSchema = readJson(join(timelineDir, "ops.v1.schema.json"));
const tools: Json[] = contract.tools;
const byName = (name: string): Json => {
  const tool = tools.find((t) => t.name === name);
  if (!tool) throw new Error(`no tool ${name}`);
  return tool;
};

// The ops schema is registered first, so that the $ref of edit_timeline resolves, as the Go side inlines it.
const ajv = new Ajv2020({ strict: true, strictTuples: false, allErrors: true });
ajv.addSchema(opsSchema);
const validators = new Map(tools.map((t) => [t.name as string, ajv.compile(t.input_schema)]));
const check = (name: string, input: unknown) => (validators.get(name) as (i: unknown) => boolean)(input);

const ASSET = "ast_01j9z3x7m8c4v6n2q5r0t1y8ab";

describe("editor tools contract", () => {
  it("lists the tools of the design note, in order", () => {
    expect(contract.version).toBe("editor-tools/v1");
    expect(tools.map((t) => t.name)).toEqual([
      "get_brief",
      "list_footage",
      "view_clip",
      "find_moments",
      "get_timeline",
      "edit_timeline",
      "render_preview",
      "finish",
    ]);
  });

  it("gives every tool a description, a strict flag and a closed object schema", () => {
    for (const t of tools) {
      expect(t.description.length, t.name).toBeGreaterThan(40);
      expect(typeof t.strict, t.name).toBe("boolean");
      expect(t.input_schema.type, t.name).toBe("object");
      expect(t.input_schema.additionalProperties, t.name).toBe(false);
    }
  });

  it("compiles every schema", () => {
    expect([...validators.keys()]).toEqual(tools.map((t) => t.name));
  });

  // What the API supports under strict tool use: no numeric or string constraints, no maxItems, no oneOf. edit_timeline is the
  // one tool that is not strict, because the edit ops use all of those.
  const allowed = new Set([
    "type",
    "properties",
    "required",
    "additionalProperties",
    "items",
    "enum",
    "const",
    "description",
    "title",
    "anyOf",
    "allOf",
    "$defs",
    "$ref",
    "default",
  ]);
  it("keeps strict tools to the keywords strict tool use supports", () => {
    for (const t of tools.filter((x) => x.strict)) {
      const walk = (node: unknown, path: string) => {
        if (Array.isArray(node)) return node.forEach((n, i) => walk(n, `${path}/${i}`));
        if (node === null || typeof node !== "object") return;
        const obj = node as Json;
        for (const [k, v] of Object.entries(obj)) {
          if (path.endsWith("/properties")) {
            walk(v, `${path}/${k}`);
            continue;
          }
          expect(allowed.has(k), `${t.name}${path}: keyword ${k}`).toBe(true);
          walk(v, `${path}/${k}`);
        }
        if (obj.type === "object") expect(obj.additionalProperties, `${t.name}${path}`).toBe(false);
      };
      walk(t.input_schema, "");
    }
    expect(tools.filter((t) => !t.strict).map((t) => t.name)).toEqual(["edit_timeline"]);
  });
});

describe("tool arguments", () => {
  it("accepts no arguments for the tools that take none, and nothing else", () => {
    for (const name of ["get_brief", "list_footage", "get_timeline", "render_preview"]) {
      expect(check(name, {}), name).toBe(true);
      expect(check(name, { x: 1 }), name).toBe(false);
    }
  });

  it("view_clip takes an asset id and optional times", () => {
    expect(check("view_clip", { asset_id: ASSET })).toBe(true);
    expect(check("view_clip", { asset_id: ASSET, at_ms: [0, 1500, 9999] })).toBe(true);
    expect(check("view_clip", {})).toBe(false);
    expect(check("view_clip", { asset_id: 1 })).toBe(false);
    expect(check("view_clip", { asset_id: ASSET, at_ms: ["1"] })).toBe(false);
    expect(check("view_clip", { asset_id: ASSET, at_ms: [1.5] })).toBe(false);
    expect(check("view_clip", { asset_id: ASSET, other: true })).toBe(false);
  });

  it("find_moments takes optional filters and nothing else", () => {
    expect(check("find_moments", {})).toBe(true);
    expect(check("find_moments", { asset_id: ASSET, kind: "rep", min_score: 0.7, limit: 5 })).toBe(true);
    expect(check("find_moments", { kind: "hold" })).toBe(true);
    expect(check("find_moments", { kind: "any" })).toBe(true);
    expect(check("find_moments", { kind: "pose" })).toBe(false);
    expect(check("find_moments", { limit: 1.5 })).toBe(false);
    expect(check("find_moments", { min_score: "high" })).toBe(false);
    expect(check("find_moments", { query: "row" })).toBe(false);
  });

  it("finish takes a summary", () => {
    expect(check("finish", { summary: "Done." })).toBe(true);
    expect(check("finish", { summary: "Done.", no_edit_needed: true })).toBe(true);
    expect(check("finish", { summary: "Done.", no_edit_needed: "yes" })).toBe(false);
    expect(check("finish", {})).toBe(false);
    expect(check("finish", { summary: "x", open_questions: [] })).toBe(false);
  });
});

describe("edit_timeline", () => {
  const validOps = readdirSync(join(timelineDir, "fixtures", "ops", "valid"))
    .filter((f) => f.endsWith(".json"))
    .sort();
  const invalidOps = readdirSync(join(timelineDir, "fixtures", "ops", "invalid"))
    .filter((f) => f.endsWith(".json"))
    .sort();

  it("refers to the op schema by its id", () => {
    expect(byName("edit_timeline").input_schema.properties.ops.items.$ref).toBe(opsSchema.$id);
  });

  it.each(validOps)("takes the valid op %s", (f) => {
    const op = readJson(join(timelineDir, "fixtures", "ops", "valid", f));
    expect(check("edit_timeline", { base_version: 0, ops: [op] })).toBe(true);
  });

  it.each(invalidOps)("refuses the invalid op %s", (f) => {
    const op = readJson(join(timelineDir, "fixtures", "ops", "invalid", f));
    expect(check("edit_timeline", { base_version: 0, ops: [op] })).toBe(false);
  });

  it("wants a base version, 1 to 100 ops and nothing besides them", () => {
    const op = { op: "set_look", preset: "bw" };
    expect(check("edit_timeline", { base_version: 0 })).toBe(false);
    expect(check("edit_timeline", { ops: [op] })).toBe(false);
    expect(check("edit_timeline", { base_version: 1.5, ops: [op] })).toBe(false);
    expect(check("edit_timeline", { base_version: 3, ops: [] })).toBe(false);
    expect(check("edit_timeline", { base_version: 3, ops: Array(100).fill(op) })).toBe(true);
    expect(check("edit_timeline", { base_version: 3, ops: Array(101).fill(op) })).toBe(false);
    expect(check("edit_timeline", { base_version: 3, ops: [op], project_id: "x" })).toBe(false);
    expect(check("edit_timeline", { base_version: 3, ops: op })).toBe(false);
  });
});
