# Design note: timeline v1, edit ops and renderer (P3)

> Status: **accepted for P3** · 2026-10-16 · Applies [D-02](../05-decisions.md#d-02-edit-representation-declarative-timeline-json--deterministic-renderer), [D-03](../05-decisions.md#d-03-music-cue-only-by-default) and [D-24](../05-decisions.md#d-24-public-ids-are-typeids-over-uuidv7). Extends [architecture §6](../03-architecture.md#6-timeline-spec-and-renderer). Each P3 task is briefed from this note. If a task has to break a rule here, it changes this note in the same PR.

## 1. Where things live

| What | Where | Notes |
|---|---|---|
| JSON Schema (the source of truth) | `packages/schemas/timeline/v1.schema.json` | Draft 2020-12, `$id` `https://contentfabrique.app/schemas/timeline/v1.json` |
| Fixtures | `packages/schemas/timeline/fixtures/{valid,invalid}/*.json` | Each invalid fixture names the issue code it must raise, in `"$comment"` (`expect: gap`); `render.txt` lists the valid ones that are also render fixtures; `assets.json` is the asset catalog (`asset_id` to `duration_ms`) that the semantic fixtures assume |
| Go types, validation, ops, diff | `apps/server/internal/timeline` | The schema is copied in by `task gen` and embedded with `go:embed`. CI fails if the copy differs |
| Beat utilities | `apps/server/internal/timeline/beats` | P3-09 |
| Compiler, profiles, ASS, time map, contact sheet | `apps/server/internal/render` | P3-03 to P3-06 |
| TS types | `packages/schemas/timeline/v1.ts`, generated from the schema | `json-schema-to-typescript`; CI drift check |

The Go types are written by hand rather than generated: generated Go from JSON Schema is poor for tagged unions. A test decodes every valid fixture into the Go types with unknown fields disallowed, encodes it again and compares the two semantically, so the types and the schema can't drift apart. Schema validation in Go uses `github.com/santhosh-tekuri/jsonschema/v6` (Apache-2.0, pure Go).

## 2. The spec

The shape in architecture §6 stands. These are the rules it leaves open.

**Top level.** `schema` is the constant `contentfabrique.timeline/v1`. `output` has `format` (`9:16`, `1:1`, `4:5` or `16:9`, [D-25](../05-decisions.md#d-25-output-formats-916-11-45-and-169-per-project)), `width` and `height` (which must equal the format's size: 1080×1920, 1080×1080, 1080×1350 or 1920×1080), `fps` (one of 24, 25, 30, 60) and `background` (`#rrggbb`). `tracks` and `markers` are arrays, and `meta` is an optional object with `idea_id` and `concept_id`. Unknown properties are rejected everywhere (`additionalProperties: false`), so a typo from the agent fails loudly.

**Tracks.** Exactly one `video` track, and its id is `main`. At most one `text` track, at most one `captions` track and at most one `audio` track. Overlay video tracks come later. The schema enforces the counts with `contains`/`maxContains`, and tells the track types apart by `type`.

**Required fields and defaults.** The schema requires what has no sensible default: on a video item `id`, `asset_id`, `source` and `at_ms`; on a text item `id`, `at_ms`, `duration_ms`, `text` and `style`; on a captions track `from`, `style` and `enabled`; on every audio item its kind's fields, `at_ms`, `source_offset_ms` and `export`; on a marker `at_ms` and `kind`. Everything else is optional and the compiler applies the default named in the tables: `speed` 1, `reframe` as `cover` at focus 0.5/0.5 and zoom 1, `look` none, `transition_in` a cut, `audio` silent, `animation` none, `strength` 1. An empty `main` track is structurally valid and fails the duration check. Times are at most 2 147 483 647 ms, so they fit a Go `int` on every platform.

**IDs.**
- Item and track ids match `^[a-z][a-z0-9_]{0,31}$` and are unique across the whole spec. The agent and the ops name items by these ids.
- References to platform objects (`asset_id`, `moment_id`, `trend_id`, `idea_id`, `concept_id`, `license_id`) are TypeIDs ([D-24](../05-decisions.md#d-24-public-ids-are-typeids-over-uuidv7)) with a fixed prefix per field: `ast`, `mom`, `trd`, `ide`, `con`, `lic`.

**Time.** Every time is an integer number of milliseconds ≥ 0. No floats.

**Video items** (`main`):

| Field | Rule |
|---|---|
| `source.in_ms`, `source.out_ms` | `0 ≤ in < out ≤ asset duration` |
| `at_ms` | See contiguity below |
| `speed` | 0.25 to 4.0, default 1.0 |
| `reframe.mode` | `cover`, `contain` or `blur_fill`; `focus.x`, `focus.y` in [0, 1] (default 0.5); `zoom` 1.0 to 3.0 |
| `look.preset` | `none`, `punchy`, `warm`, `cool`, `bw`, `matte` |
| `transition_in` | `{ "type": "cut" }` or `{ "type": "crossfade" \| "dip_to_black" \| "whip", "duration_ms": 100–1000 }`. The first item must be a cut (or have none); the schema checks that with a one-element `prefixItems` |
| `audio` | `use_source` (default false) and `gain_db` (−30 to +6) |
| `moment_id`, `why` | Optional. `why` is at most 280 characters and is never rendered |

An item's **output duration** is `round((out_ms − in_ms) / speed)`.

**Contiguity on `main`.**
- The items are sorted by `at_ms`, the first starts at 0, and each next item starts at the previous item's end minus its own transition duration.
- Gaps are errors, and so are overlaps that no transition explains.
- `at_ms` is redundant with the durations on purpose: it keeps diffs readable and lets the UI place items without computing anything. The ops keep it consistent (§4).

**Text items** (`text` track): `at_ms`, `duration_ms` (≥ 300), `text` (1 to 120 characters, at most 3 lines: the schema counts lines by `\n` with a `pattern`, and characters as code points, so Go must count runes, not bytes), `style` (an id from the style registry, §5), `anchor` (`upper_third`, `center`, `lower_third`) or `position` `{x, y}` in [0, 1] (exactly one of the two), and `animation` (`none`, `pop_in`, `fade`, `typewriter`, `slide_up`).

**Captions track:** `from: "transcript"`, `style` (`word_highlight` or `chunk`), `enabled`, `uppercase` (default true) and `max_words` (1 to 4, default 2).

**Audio items:** `export` is required on every item, and each kind's variant fixes it (`cue_only` for `trend_sound`, `licensed` for the other two).

| `kind` | Fields | `export` |
|---|---|---|
| `trend_sound` | `trend_id`, `source_offset_ms` | Must be `cue_only`. The schema enforces it twice: the variant's `const`, and an `if`/`then` on `kind` whose error points straight at `/export` |
| `licensed_track` | `asset_id`, `license_id`, `source_offset_ms` | `licensed` |
| `creator_audio` | `asset_id`, `source_offset_ms` (the creator's own upload, with a rights attestation) | `licensed` |

Every audio item has `at_ms`, an optional `duration_ms` (default: to the end of the timeline), `gain_db` (−30 to +6) and an optional `fade_in_ms` and `fade_out_ms`. Audio items may overlap, because they are mixed.

**Markers:** `at_ms`, `kind` (`beat`, `downbeat`, `drop`, `section`) and `strength` in [0, 1].

## 3. Validation

`timeline.Validate(spec []byte, assets AssetCatalog) Result` runs in two layers:

1. **Structure.** The JSON Schema rejects wrong types, ranges and enums, unknown fields, the format/size pairing, track counts, text with both or neither of `anchor` and `position`, a first clip that is not a cut, text over 3 lines and the `trend_sound` export rule. Track, audio-item and transition types are `oneOf` unions, so one mistake yields errors for every branch. The Go layer reports only the branch whose discriminator (`type`, `kind`) matches, so the agent sees one message per problem.
2. **Semantics.** Go checks what the schema can't express: unique ids, contiguity (including the first item starting at 0), `0 ≤ in_ms < out_ms ≤` the asset's duration (from the catalog), minimum shot length (an output duration ≥ 300 ms), total duration from 1 s to 180 s, every text item, audio item and marker inside the total duration, text inside the safe zone (§5), and transitions shorter than both items they join.

`Result` holds `[]Issue{Code, Path, Message, Severity}`:
- `Path` is a JSON Pointer into the spec, such as `/tracks/0/items/3/source/out_ms`.
- `Severity` is `error` or `warning`. Only errors block a version.
- `Code` comes from a closed, documented list, such as `schema`, `duplicate_id`, `gap`, `overlap`, `source_out_of_range`, `shot_too_short`, `duration_out_of_range`, `outside_timeline`, `unsafe_text`, `unknown_asset` and `transition_too_long`. Style ids that the registry doesn't know get `unknown_style` once P3-04 adds the registry. The editor agent sees the codes and messages, so they are written for a model to act on: "item c3 ends at 5400 ms but asset ast_… is 5100 ms long".

`AssetCatalog` is an interface (`Duration(assetID) (ms int, ok bool)`). Tests use an in-memory map.

## 4. Edit ops and versions (P3-02)

- **Ops are JSON objects** with an `op` field: `add_clip`, `remove_item`, `move_item`, `trim_item`, `replace_source`, `set_speed`, `set_reframe`, `add_text`, `update_text`, `remove_text`, `set_transition`, `set_sound`, `remove_sound`, `snap_cuts_to_beats`, `set_look` and `set_captions`. Each op has its own JSON Schema in `packages/schemas/timeline/ops.v1.schema.json`. The editor agent's `edit_timeline` tool takes a batch of ops, so these schemas become its tool contract in P4-02.
- **`Apply(spec, ops) (spec', Result)` is atomic and pure.** It applies the whole batch, re-runs `Validate`, and returns either a new spec or the issues with nothing changed.
- **The ops ripple on `main`.** A trim, insert, removal, move or speed change recomputes `at_ms` for every later main item. Text and audio items keep their absolute times. A text item that now overruns the end triggers an `outside_timeline` error, so the agent fixes it on purpose.
- **New item ids.** `add_*` ops accept an optional `id`. Without one, the engine assigns the next free id for the kind (`c7`, `t3`, `s2`). Assignment is deterministic, which keeps replays and golden tests stable.
- **Versions are immutable.** The timeline version table (P3-06 adds it with its migration) stores `version`, `parent_version`, `spec`, the applied `ops`, `diff`, `author_kind` (`agent` or `user`) and `author_id`.
- **The diff is an RFC 6902 JSON Patch** from the parent to the child. The property test checks `patch(parent, diff) == child` and that a random op sequence never yields a spec that fails validation (it either applies cleanly or returns issues). `Summarize(diff)` gives a one-line human summary per change ("trimmed c3 by 400 ms at the end"), used by the run feed and the version list.

## 5. Text, styles and safe zones (P3-04)

- **Safe zones are per format** (D-25). For 9:16 the zone follows [research §3.4](../02-research.md#34-captions-safe-zones-beat-snapping): keep out of the top 240 px, the bottom 660 px and 120 px on each side, and out of the right-hand action column (x > 900 for y > 840). 1:1 and 4:5 keep a 5 % margin on every side; 16:9 keeps a 10 % title-safe margin and the bottom 15 % clear. They are Go values (`render.SafeZones[format]`), so a later profile can tighten them.
- **During validation (P3-01)**, the three anchors are boxes inside the safe zone by construction. A `position` gets an estimated box: the style's line height × the line count, and a width of 0.6 em per character, capped at the safe width. P3-04 replaces the estimate with libass font metrics and keeps the same issue code.
- **The style registry** (`render/styles`) maps a style id to a font file, size, colours, outline and animation defaults. Fonts come from an allow-list of OFL-licensed files vendored under `packages/media-tools/fonts` with their licence texts ([D-16](../05-decisions.md#d-16-licensing-policy-for-ml-and-media-components)). The spec can't name a font directly.
- **Rendering:** all text and captions become one generated `.ass` file per render, burned in with the `ass` filter, with `fontsdir` pointing at the vendored fonts. Caption timing comes from the transcript's word timestamps, mapped through the time map to output time, and is snapped to frames.

## 6. Compiler and renderer (P3-03, P3-05, P3-06)

**Pure compile, separate execution.** `render.Compile(spec, profile, resolver) (Plan, error)` returns:
- the ffmpeg argument list,
- the generated ASS text,
- the time map,
- the frame-snapped cut list.

It never runs ffmpeg. `render.Execute(ctx, plan, sandbox)` runs it later, through the media package and the sandbox provider from P1-03. Golden tests can then assert on the plan itself, quickly and without ffmpeg.

**Frame snapping:**
- `frame(ms) = floor(ms × fps / 1000 + 0.5)`.
- Item boundaries are snapped from the cumulative `at_ms`, never from summed durations, so rounding can't drift over a long timeline.
- Item `i` covers frames `[frame(at_i), frame(at_{i+1}))`.

**Video graph per main item:** `trim` on the source frames → `setpts=PTS-STARTPTS` → `setpts=PTS/speed` → `fps` → reframe → look → `format=yuv420p` → `settb`.
- **Reframe `cover`** scales to fill, then crops around `focus` (clamped so the crop stays inside the frame), then applies `zoom` as an extra centred crop and scale.
- **`contain`** pads with `background`.
- **`blur_fill`** places a blurred, scaled copy behind a `contain`.
- **Joining items:** a plain `concat` when every transition is a cut; otherwise an `xfade` chain with offsets from the snapped cut list. `whip` is an `xfade` `slideleft` with a short blur.
- **Looks** are fixed `eq` and `curves` settings per preset. No external LUT files in v1.

**Audio graph:**
- **Source audio** from main items with `use_source`: `atrim` → `asetpts` → `atempo` chained to cover speeds outside 0.5–2 → `volume`.
- **Audio-track items:** `atrim` from `source_offset_ms` → `afade` → `adelay` to `at_ms` → `volume`.
- **Mix and normalise:** everything is mixed with `amix=normalize=0`, then `loudnorm` targets I = −14 LUFS and TP = −1 dBTP. The final profile runs two passes; the preview profile runs a single dynamic pass.
- **Cue-only sounds are never reproduced.** `cue_only` items are dropped from the graph **in every profile**. Compile collects the cue (`trend_id`, `at_ms`, `source_offset_ms`) into `Plan.SoundCues` for the export package instead.
- **The preview beat guide.** The preview profile adds a generated beat guide built from the markers: 30 ms sine clicks at 1 kHz for `beat`, 1.5 kHz for `downbeat` and a longer accent for `drop`, at −12 dB. A test compiles a spec with a `cue_only` item under every profile and asserts that its source never appears among the inputs (D-03).

**Profiles:**

| Profile | Size | Source | Video | Audio | Extras |
|---|---|---|---|---|---|
| `preview` | half the format's size (540×960 for 9:16) | proxies (`proxy_720.mp4`) | x264 `veryfast`, CRF 28 | AAC 128k | Watermark, beat guide |
| `final` | the format's size | originals | x264 `medium`, CRF 18, `high` profile | AAC 192k, 48 kHz | `+faststart` |

Both profiles write `yuv420p` and add `-map_metadata -1` and `-fflags +bitexact`, so the output is deterministic.

**Outputs of a render (P3-06):**
- **The MP4.**
- **`time_map`:** a JSON array of `{out_start_ms, out_end_ms, item_id, asset_id, source_in_ms, speed}` for main items, plus `{out_start_ms, out_end_ms, item_id}` for text items. A comment at output time `t` resolves to the main item that covers `t`, and to the source time `source_in_ms + (t − out_start_ms) × speed`.
- **The contact sheet:** one frame 100 ms after each cut plus the last frame, tiled 4 across with the output timestamp drawn on each tile, as JPEG. The editor agent checks its own work from this sheet.
- **Progress events:** parsed from `-progress pipe:1`.

## 7. Tests

**Fixtures (P3-01):** 30 valid and 30 invalid specs, covering every issue code and every format at least once. The valid ones are structurally and semantically valid against `fixtures/assets.json`. Invalid fixtures that expect `schema` must fail the JSON Schema; the ones that expect a semantic code must pass it and are caught by the Go layer. `packages/schemas` tests the first half with Ajv (2020-12, strict mode) and checks that `render.txt` covers every format, transition, reframe mode, look, audio kind and both ends of the speed range; the Go tests check the second half and that the Go types round-trip every valid fixture.

**Golden plans (P3-03):** 20 of the valid fixtures are also render fixtures (listed in `fixtures/render.txt`, covering every format, transition, reframe mode, look, speed range and audio kind). For each of them, the compiled argument list and ASS text are snapshot-tested as text files under `testdata/golden/`. `go test ./internal/render -update` rewrites the snapshots, and a reviewer reads the diff.

**Golden renders (P3-03):**
- **Media:** the test media is generated when the tests run (`testsrc2`, `sine` and a coloured countdown with a burned-in frame number), so no binary fixtures are committed. It's cached in `testdata/.cache`.
- **Comparison:** each render is checked at sampled frames, with an SSIM ≥ 0.98 against reference frames committed as small PNGs. The frame-number overlay makes an off-by-one cut fail.
- **Where they run:** ffmpeg 8 is pinned in CI, using the media-tools image once P1-03 lands and a pinned static build until then. The tests skip, with a clear message, when ffmpeg is missing locally.

**Timing (P3-06):** a 30 s edit renders as a preview in ≤ 10 s and as a final in ≤ 60 s, measured on the target host class and reported in the PR. A CI timing test is too noisy to gate on.

## 8. Order and boundaries

P3-01 schema and validation → P3-09 beat utilities → P3-02 ops (including `snap_cuts_to_beats`, which uses P3-09) → P3-03 compiler → P3-04 text and P3-05 audio → P3-06 render jobs (needs River from P0-06 and storage from P1-01) → P3-07 and P3-08 web (need the API from P0-08). P3-07 includes the range-selection timeline under the player and the comment history side panel ([plan, P3-07](../04-implementation-plan.md#p3--timeline-spec-and-renderer--34-weeks-parallel-with-p1p2)).

Effect presets ([D-26](../05-decisions.md#d-26-video-effects-are-presets)) arrive as additive v1 fields: `motion` on video items and new transition types (P3-10), and a `graphics` track whose items reference a template (P4-11).

Out of scope for v1: overlay video tracks, keyframed motion, LUT files, the in-browser live preview and HyperFrames titles. The schema leaves room for them: new track types and new enum values are additive changes inside `v1`. Anything that changes the meaning of an existing field needs `v2`.
