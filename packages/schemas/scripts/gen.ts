// Generates timeline/v1.ts from timeline/v1.schema.json. Run with `bun run gen`; commit the result.
// CI runs scripts/gen-check.sh, which fails when the committed file differs from a fresh run.
import { readFileSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { compile } from "json-schema-to-typescript";

const dir = join(dirname(fileURLToPath(import.meta.url)), "..", "timeline");
const schema = JSON.parse(readFileSync(join(dir, "v1.schema.json"), "utf8"));

const ts = await compile(schema, "Timeline", {
  // The schema closes every object itself; do not add index signatures where it did not.
  additionalProperties: false,
  // Emit a named type for every $defs entry, not only the ones the root reaches.
  unreachableDefinitions: true,
  bannerComment: [
    "/* eslint-disable */",
    "/**",
    " * This file was generated from v1.schema.json by json-schema-to-typescript (`bun run gen` in packages/schemas).",
    " * Do not edit it by hand: change the schema and regenerate. CI fails when this file is out of date.",
    " */",
  ].join("\n"),
  cwd: dir,
});

writeFileSync(join(dir, "v1.ts"), ts);
console.log("wrote timeline/v1.ts");
