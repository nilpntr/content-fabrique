// Generates timeline/v1.ts from timeline/v1.schema.json and timeline/ops.v1.ts from timeline/ops.v1.schema.json.
// Run with `bun run gen`; commit the result. CI runs scripts/gen-check.sh, which fails when a committed file differs
// from a fresh run.
import { readFileSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { compile } from "json-schema-to-typescript";

const dir = join(dirname(fileURLToPath(import.meta.url)), "..", "timeline");

async function generate(schemaFile: string, outFile: string, rootName: string) {
  const schema = JSON.parse(readFileSync(join(dir, schemaFile), "utf8"));
  const ts = await compile(schema, rootName, {
    // The schemas close every object themselves; do not add index signatures where they did not.
    additionalProperties: false,
    // Emit a named type for every $defs entry, not only the ones the root reaches.
    unreachableDefinitions: true,
    bannerComment: [
      "/* eslint-disable */",
      "/**",
      ` * This file was generated from ${schemaFile} by json-schema-to-typescript (\`bun run gen\` in packages/schemas).`,
      " * Do not edit it by hand: change the schema and regenerate. CI fails when this file is out of date.",
      " */",
    ].join("\n"),
    cwd: dir,
  });
  writeFileSync(join(dir, outFile), ts);
  console.log(`wrote timeline/${outFile}`);
}

await generate("v1.schema.json", "v1.ts", "Timeline");
await generate("ops.v1.schema.json", "ops.v1.ts", "EditOp");
