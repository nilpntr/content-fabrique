// Generates api/schema.ts (the TypeScript types of the OpenAPI contract) from openapi.yaml. Run with `bun run gen:api`
// (or `task gen:api`, which also regenerates the Go side); commit the result. CI regenerates both and fails on a diff.
//
// Operations tagged `webhooks` are called by other services, not by the web app, so they are left out of the types.
import { readFileSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import openapiTS, { astToString, type OpenAPI3 } from "openapi-typescript";
import { parse } from "yaml";

const root = join(dirname(fileURLToPath(import.meta.url)), "..");
const spec = parse(readFileSync(join(root, "openapi.yaml"), "utf8")) as OpenAPI3;

const HTTP_METHODS = ["get", "put", "post", "delete", "options", "head", "patch", "trace"];
const EXCLUDED_TAGS = new Set(["webhooks"]);

type Operation = { tags?: string[] };
type PathItem = Record<string, Operation | undefined>;

const paths = (spec.paths ?? {}) as Record<string, PathItem>;
for (const [path, item] of Object.entries(paths)) {
  for (const method of HTTP_METHODS) {
    if (item[method]?.tags?.some((tag) => EXCLUDED_TAGS.has(tag))) delete item[method];
  }
  if (!HTTP_METHODS.some((method) => item[method])) delete paths[path];
}
spec.tags = spec.tags?.filter((tag) => !EXCLUDED_TAGS.has(tag.name));

const banner = [
  "/**",
  " * This file was generated from openapi.yaml by openapi-typescript (`bun run gen:api` in packages/schemas, or `task gen:api`).",
  " * Do not edit it by hand: change the spec and regenerate. CI fails when this file is out of date.",
  " * Operations tagged `webhooks` are not part of it.",
  " */",
  "",
].join("\n");

const ast = await openapiTS(spec);
writeFileSync(join(root, "api", "schema.ts"), banner + astToString(ast));
console.log("wrote api/schema.ts");
