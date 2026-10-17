# @cf/schemas

JSON Schemas shared by the Go server and the web app. Today that is the timeline spec v1 (P3-01); `openapi.yaml` (P0-08) and the edit-op schemas (`ops.v1.schema.json`, P3-02) land here later.

The rules of the spec are in the [timeline design note](../../docs/design/timeline-v1.md). The schema is the source of truth for structure; read its `title` and `description` fields, because the editor agent will use them as tool documentation.

## Layout

```
timeline/
  v1.schema.json        JSON Schema draft 2020-12, $id https://contentfabrique.app/schemas/timeline/v1.json
  v1.ts                 generated TypeScript types (committed, do not edit)
  v1.test.ts            Ajv tests: every fixture, the render-fixture coverage and the edge rules
  fixtures/
    valid/*.json        30 specs that pass the schema (and the Go semantic checks, given assets.json)
    invalid/*.json      30 specs; "$comment": "expect: <code>" names the issue code each must raise
    render.txt          the 20 valid fixtures that are also render fixtures (P3-03)
    assets.json         the asset catalog the semantic fixtures assume (asset_id -> duration_ms)
scripts/
  gen.ts                writes timeline/v1.ts
  gen-check.sh          the CI drift check
```

## Commands

Run from the repo root (or `cd packages/schemas`).

| Command | What it does |
|---|---|
| `bun run --filter @cf/schemas test` | Validates every fixture with Ajv (2020-12, strict mode) |
| `bun run --filter @cf/schemas typecheck` | Type-checks the tests, the scripts and the generated types |
| `bun run --filter @cf/schemas gen` or `task gen:schemas` | Regenerates `timeline/v1.ts` from the schema with `json-schema-to-typescript` |
| `bun run --filter @cf/schemas gen:check` | Regenerates, then fails if `timeline/v1.ts` differs from what is committed (CI runs this) |

After you change `v1.schema.json`, run `gen` and commit `v1.ts` with it.

## Structure here, semantics in Go

The JSON Schema checks structure only: types, ranges, enums, unknown fields, TypeID prefixes, the format/size pairing, track counts, the anchor-or-position rule, the first clip being a cut and the `trend_sound` => `cue_only` rule.

Everything that needs to compare values lives in Go (`apps/server/internal/timeline`, P3-01 part 2): unique ids, contiguity on `main`, source ranges against the asset catalog, shot length, total duration, items inside the timeline, text in the safe zone, unknown assets and transitions longer than their clips. Do not try to express those in the schema. The list is in the schema's top-level `$comment` and in [design note section 3](../../docs/design/timeline-v1.md#3-validation).

## Fixtures

- A valid fixture has no `$comment`. The schema allows `$comment` at the top level only, for the invalid fixtures.
- An invalid fixture that expects `schema` must fail the JSON Schema. One that expects any other code (`gap`, `overlap`, `duplicate_id`, `source_out_of_range`, `shot_too_short`, `duration_out_of_range`, `outside_timeline`, `unsafe_text`, `unknown_asset`, `transition_too_long`) must pass it: the Go layer catches it.
- Ids in the fixtures are real TypeIDs (lowercase prefix, then 26 characters of Crockford base32 whose first character is 0 to 7).
- Adding a fixture: add the file, keep the counts the test expects (or change them on purpose), and add it to `render.txt` only if it should be rendered.

## Additive changes

New optional fields (`motion`, D-26), new enum values (more transition types) and new track types (`graphics`) are additive inside `v1`. They must not change the meaning of an existing field; that needs `v2`.
