# @cf/schemas

JSON Schemas shared by the Go server and the web app. Today that is the OpenAPI 3.1 contract of the Go API (`openapi.yaml`, P0-08), the timeline spec v1 (P3-01), its edit ops (P3-02) and the tool contract of the editor agent (P4-02a).

The rules of the spec are in the [timeline design note](../../docs/design/timeline-v1.md). The schema is the source of truth for structure; read its `title` and `description` fields, because the editor agent will use them as tool documentation.

## Layout

```
openapi.yaml            OpenAPI 3.1: the source of truth of the Go API (servers, bearerAuth, Error + codes, TypeID patterns, the endpoints)
redocly.yaml            lint rules for the spec
api/
  schema.ts             generated TypeScript types of the spec (committed, do not edit); imported as @cf/schemas/api
timeline/
  v1.schema.json        JSON Schema draft 2020-12, $id https://contentfabrique.app/schemas/timeline/v1.json
  v1.ts                 generated TypeScript types (committed, do not edit)
  v1.test.ts            Ajv tests: every fixture, the render-fixture coverage and the edge rules
  ops.v1.schema.json    the edit ops (one definition per op, discriminated on "op"; $defs/opBatch is a batch), $id https://contentfabrique.app/schemas/timeline/ops.v1.json
  ops.v1.ts             generated TypeScript types of the ops (committed, do not edit)
  ops.v1.test.ts        Ajv tests: the ops fixtures, the definitions shared with v1.schema.json, batch limits
  fixtures/ops/
    valid/*.json        35 single ops that pass the schema (every op at least once)
    invalid/*.json      42 single ops that fail it
  fixtures/
    valid/*.json        31 specs that pass the schema (and the Go semantic checks, given assets.json)
    invalid/*.json      46 specs, each of which must raise exactly one issue code (named in expect.json)
    expect.json         invalid fixture file -> { code, path?, keyword? }
    render.txt          the 20 valid fixtures that are also render fixtures (P3-03)
    assets.json         the asset catalog the semantic fixtures assume (asset_id -> duration_ms)
agents/
  editor-tools.v1.json  the editor agent's tools as the model sees them: name, description, strict flag and input schema (edit_timeline refers to the ops schema by its $id); the Go package internal/agents/editor embeds a copy
  editor-tools.v1.test.ts  Ajv tests: the schemas compile, accept and refuse the right arguments, strict tools stay inside what strict tool use supports
scripts/
  gen.ts                writes timeline/v1.ts and timeline/ops.v1.ts
  gen-api.ts            writes api/schema.ts from openapi.yaml (operations tagged webhooks are left out)
  gen-check.sh          the CI drift check
```

## Commands

Run from the repo root (or `cd packages/schemas`).

| Command | What it does |
|---|---|
| `bun run --filter @cf/schemas test` | Validates every fixture with Ajv (2020-12, strict mode) |
| `bun run --filter @cf/schemas typecheck` | Type-checks the tests, the scripts and the generated types |
| `bun run --filter @cf/schemas gen` or `task gen:schemas` | Regenerates `timeline/v1.ts` and `timeline/ops.v1.ts` from the schemas with `json-schema-to-typescript` |
| `bun run --filter @cf/schemas gen:check` | Regenerates, then fails if either generated file differs from what is committed (CI runs this) |

After you change `v1.schema.json` or `ops.v1.schema.json`, run `gen` and commit `v1.ts` / `ops.v1.ts` with it; for the ops schema also run `task gen:timeline` (or `go generate ./internal/timeline/...` in apps/server) to refresh the copy the Go package embeds, and commit that too. The ops definitions repeat the shared ones of `v1.schema.json`; `ops.v1.test.ts` fails if they drift apart.

## The API contract

`openapi.yaml` is the source of truth for the Go API ([architecture section 9](../../docs/03-architecture.md#9-api) says how to add an endpoint). `task gen:api` (from the repo root) regenerates both generated sides: `apps/server/internal/api/api.gen.go` (oapi-codegen) and `api/schema.ts` (openapi-typescript). `task lint:api` or `bun run --filter @cf/schemas lint:api` lints the spec with Redocly. Commit the generated files with the spec: the `contract` job of CI regenerates them and fails on any difference.

## Structure here, semantics in Go

The JSON Schema checks structure only: types, ranges, enums, unknown fields, TypeID prefixes, the format/size pairing, track counts, the anchor-or-position rule, the first clip being a cut and the `trend_sound` => `cue_only` rule.

Everything that needs to compare values or look something up lives in Go (`apps/server/internal/timeline`, P3-01 part 2): unique ids, contiguity on `main`, source ranges against the asset catalog, shot length, total duration, items inside the timeline, text in the safe zone, unknown assets, audio that is not licensed or attested, fades longer than their sound and transitions longer than their clips. Do not try to express those in the schema. The list is in the schema's top-level `$comment` and in [design note section 3](../../docs/design/timeline-v1.md#3-validation).

## Fixtures

- Fixtures are plain specs. The schema is closed (no `$comment` property anywhere), so expectations live in `fixtures/expect.json`, which the TS test and the Go tests both read.
- An invalid fixture that expects `schema` must fail the JSON Schema, and Ajv must report an error at the `path` (and `keyword`, where given) in `expect.json`. One that expects any other code (`gap`, `overlap`, `duplicate_id`, `source_out_of_range`, `shot_too_short`, `duration_out_of_range`, `outside_timeline`, `unsafe_text`, `unknown_asset`, `transition_too_long`, `unlicensed_audio`, `fade_too_long`, `wrong_asset_kind`, `first_transition`) must pass it: the Go layer catches it and raises that code only.
- `assets.json` is the catalog the semantic fixtures assume: duration, `kind` (`video`, `audio`) and `rights` (`licensed`, `attested`, `none`) per asset.
- Ids in the fixtures are real TypeIDs (lowercase prefix, then 26 characters of Crockford base32 whose first character is 0 to 7).
- Adding a fixture: add the file, keep the counts the test expects (or change them on purpose), and add it to `render.txt` only if it should be rendered.

## Additive changes

New optional fields (`motion`, D-26), new enum values (more transition types) and new track types (`graphics`) are additive inside `v1`. They must not change the meaning of an existing field; that needs `v2`.
