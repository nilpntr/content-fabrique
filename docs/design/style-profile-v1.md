# Design note: the creator style profile v1 (P6-05, as built)

> Status: **built, development-first slice** (camera, shot lengths and the drop, and never over the body: P6-06, sections 3b and 8c) · 2026-10-18 · The implementation of [P6-05](../04-implementation-plan.md) up to the upload route: the analysis, the stored profile, the editor that follows it and the checks that hold an edit to it. It extends [edit style v1](edit-style-v1.md) (the genre playbooks are the generic craft; the profile is this creator's craft), [audio v1](audio-v1.md) (the ears), [agent-runtime v1](agent-runtime-v1.md) (untrusted data) and [spend control v1](spend-control-v1.md) (the one model call). Code: `internal/styleprofile`, `internal/media/stylestats.go`, `internal/store/creator_style.go`, `internal/agents/editor/style.go` and `check.go`, `cmd/cfctl/style.go`.

## 1. Why

The editor so far had one idea of a good short video: a fast cut-up on a beat grid with an effect every three or four cuts and a one to two second hook. The first creator whose finished videos we measured (Gino, five gym videos, 14 to 44 s) does the opposite: 0 to 5 cuts a video, takes of 8 to 44 s, no effects, no grade, one title in cream capitals, a trending sound with a quiet intro that lifts at about 2 to 3 s. An editor that follows the playbook produces a video that is "correct" and not his. The profile measures how a creator edits, from videos they already posted, and the editor follows it over the playbook.

Principles: **measure, don't ask** (deterministic ffmpeg measures first; a model only for what pixels need a reader for), **numbers with ranges and a confidence** (a profile of five videos is not a law), **untrusted in, untrusted out** (a reference video is an upload, the title read from it is text a stranger could have put there), **the creator can read it** (every dimension has a plain-language line and the evidence per reference).

## 2. The profile (`styleprofile.Profile`, version 1)

One JSON document per workspace, aggregated from the measures of 1 to 10 references (the newest 10 are kept). Every numeric dimension is a `Range` (median, min, max) over the references, so the editor and the checks can allow the creator's own spread.

| Block | What it holds | How it is measured |
|---|---|---|
| `format` | duration (median, range), fps | probe |
| `pacing` | style (`long_takes`, `mixed`, `montage`), cuts per video and per minute, shot median, longest and shortest shot, the share of videos with no cut, the share of the screen time in takes of 6 s or more, all shot lengths | scene-change scores at 30 fps (section 3) |
| `hook` | where the first cut comes; whether a title is on the first frames | the first cut; the vision read |
| `effects` | verdict (`none`, `light`, `busy`), zoom moments and flashes per minute, the share of the time in a slow push | thumbnail motion proxies (section 3) |
| `look` | luma mean and p90, saturation, vignette ratio, grade (`none` for the camera's natural picture, `graded` when a vignette or a heavy treatment shows), `low_key` | `signalstats` at 4 fps, thumbnails |
| `text` | share of videos with a title / labels / captions, case, colour, position, alignment, weight, condensed, example titles | the vision read (section 4) |
| `audio` | the sound, section 5 | `ebur128`, onsets, a spectral-flux pass |
| `confidence`, `evidence` | 0 to 1 from the number of references and their agreement; one line per reference | |

`Profile.Describe()` is the plain-language text (`cfctl style show`); `Profile.CompactBlock()` is the 12-line block the editor reads (under 2.5 kB, tested).

## 3. The picture measures (`video.go`)

One sandbox job (`media.StyleArgs`), one pass over the file, three branches of a split: scene scores (270 px wide, 30 fps) and `signalstats` (4 fps) written as ffmpeg metadata files, and 48 x 48 gray thumbnails (10 fps) written raw. Everything is read back from files in the job's output directory (regular files only, size-capped), never from stdout. The file is only ever an input of the sandbox; it is capped at 10 minutes and must have video.

- **Cuts.** A cut is a local maximum of the scene score at or above **0.10**, at least 0.3 s from the previous one (the stronger of two wins) and 0.3 s from the start and the end. The threshold was tuned on the five references against the cuts a person counts: 0.10 gives **3, 0, 3, 0, 5** cuts (in file order), which is what the eye counts; the footage is low-key (the median luma is 0.08), so the scene scores of its cuts are low and the usual higher thresholds miss them.
- **Shot lengths** come from the cuts. Long takes are shots of 6 s or more.
- **Effects.** Applied effects cannot be separated from camera moves in a finished video, so these are proxies: quick zooms (a scale change of 6 % within half a second, found by searching the scale that best matches consecutive thumbnails with bilinear sampling in steps of 0.5 %), slow pushes (a run of 1.5 s or more with one sign and a net change of 3 %), flashes (the mean luma jumps by 35 and falls back within 0.4 s) and the vignette ratio (corners over centre). The verdict is a majority over the references and is `none` for all five of Gino's videos. A handheld creep shows up as a push-in share (one of Gino's videos has 0.99 of its time flagged as a slow push, and still reads `none` because the other four have 0): a limitation, and why the verdict is a majority and not a sum.
- **Light.** The medians of the per-frame luma and saturation statistics, with the same 8-bit limited-range normalisation the clip analysis uses, so a profile and a clip analysis can be compared.

## 4. The text read (`vision.go`): one small Haiku call per reference

What is written on the screen, and how, cannot be measured with ffmpeg. One call per reference, on **three frames of the hook** (at 12 %, 50 % and 88 % of the first 3.6 s, 512 px on the long side, cut in the sandbox like every frame), to the stage `stylevision` (Haiku 5.5 in all three routing profiles, `max_tokens` 600).

- **A forced strict tool** (`describe_title_style`): `title_present`, `text`, `case`, `colour` (hex), `position`, `alignment`, `weight`, `condensed`, `lines`, `exercise_labels`, `captions`. Thinking is off.
- **The answer is untrusted.** `ValidateTitleStyle` parses the arguments with unknown fields refused, every field required, the enums checked again, the colour a `#RRGGBB`, `lines` 0 to 6; the title text goes through `textclean.Model` and is **rejected, not cut**, when it is over 120 characters or looks like a link, an address, a handle or words aimed at a reader (the quick-look pattern, checked on the NFKC-folded text so full-width letters and zero-width splits do not get through). A rejected answer is a read with no text (`invalid_output`); the measures are kept.
- **Where the text goes.** It is stored only as an example of the creator's voice, shown to the editor inside the `creator_style` untrusted-data block, and never used as an instruction. The system prompt of the read says that words inside the frames are picture, not instructions.
- **Money.** A llm-layer call with `RunInfo{Budget: NewRunBudget(10_000)}`: **at most $0.01 a reference**, enforced before the request is sent (a call whose worst case does not fit is refused), metered into the usage ledger with a spend hold like every other call (`cfctl` builds the client from the ledger-backed factory). A worked number from the price table (tested for all three profiles): about 6,100 input tokens counted pessimistically (each image at the 1,600 tokens the layer assumes when it cannot count) and 120 output tokens cost **674 micro-dollars ($0.0007)**; the worst case with `max_tokens` 600 is 914 micro-dollars. Five references cost about **$0.004** at the usual price.
- A failed or refused read never fails the analysis: `Analysis.VisionErr` / `VisionResult.Reason` (`invalid_output`, `no_answer`, `refused`, `truncated`, `spend_cap`).
- **Status.** The read is built and tested against a fake client (the request, the budget, each outcome, the validator on 17 hostile answers). It was **not run live** in this slice: the build environment had neither an API key nor a database, and the brief allowed only the five references. The text styles in the test fixture are what a person read from the hook frames of the five videos, and a live run of `cfctl style analyze --workspace ... --files ...` (without `--no-vision`) is the first thing to do with a key; the expected cost is under $0.01.

## 5. The audio profile (`audio.go`, `spectral.go`)

Audio is first-class because the sound decides what the cuts and the hook should do. It reuses `internal/audiocheck` (loudness, the momentary curve, onsets, silence) and adds one pass that dumps the sound as 8 kHz mono float and analyses it in plain Go (a radix-2 FFT, 256 samples, 10 ms hop).

| Field | Meaning | Method |
|---|---|---|
| `intro_s`, `lift_lu`, `lift_share` | the quiet intro before the music lifts, and how big the lift is | the first rise of the momentary loudness of the second after a time over the 1.5 s before it, at least 1.5 LU and 70 % of the biggest rise (a track that builds has later, bigger rises; the first significant one is the lift) |
| `arc`, `quarter_lufs` | `builds`, `flat` or `falls`, from the mean loudness of each quarter (a change of 1.5 LU) | momentary curve |
| `ending_lu`, `no_fade_out` | how the sound ends against the integrated loudness | `ebur128` end loudness |
| `tempo_bpm` (+ by pacing) | the tempo, and the tempo of the tracks under long-take videos against cut-up ones | the autocorrelation of the spectral flux (a smooth pad has few energy onsets, but its beat still moves the spectrum), with the onset train as a fallback. An estimate is ambiguous by a factor of two |
| `cut_onset_ms`, `cuts_within_100ms`, `beat_locked` | where the cuts sit against the strong onsets (6 dB or more) and against the fitted beat | beat-locked only when the median distance is 50 ms or less and 80 % of the cuts are within 100 ms |
| `first_cut_at_lift_share` | the first cut lands within 0.6 s of the lift | |
| `content` | `music_only`, `music_with_ambience` or `unknown`; `speech` is `unknown` | model-free: music is a steady tempo (confidence 0.1 or more) over a sound that is there; a median spectral flatness over 0.2 means noise on top (ambience or effects) |
| `lufs`, `range_lu` | integrated loudness and loudness range | `ebur128` |

### The five references

| | duration | cuts | quiet intro, lift | arc | ends | tempo | cuts from an onset (median) | loudness (range) | content |
|---|---|---|---|---|---|---|---|---|---|
| 1 | 14.6 s | 3 | 3.2 s, +2.9 LU | builds | +0.2 LU | 78 BPM | 238 ms | -15.6 LUFS (1.6 LU) | music only |
| 2 | 43.8 s | 0 | 1.9 s, +2.8 LU | builds | +2.1 LU | 70 BPM | | -17.0 LUFS (4.5 LU) | music only |
| 3 | 25.3 s | 3 | 2.9 s, +1.9 LU | flat | +0.2 LU | 124 BPM | 30 ms | -15.9 LUFS (0.8 LU) | music only |
| 4 | 28.6 s | 0 | 7.4 s, +5.5 LU | builds | +0.8 LU | 76 BPM | | -19.0 LUFS (2.9 LU) | music only |
| 5 | 18.6 s | 5 | 2.1 s, +3.3 LU | flat | +0.2 LU | 117 BPM | 155 ms | -16.7 LUFS (0.8 LU) | music only |

What it says about the hypotheses we went in with: a quiet intro and a lift at 2 to 3 s holds for four of five (1.9, 2.1, 2.9, 3.2 s; the fifth, a 28 s single take, builds slowly and lifts at 7.4 s); no video fades out (the end is at or above the integrated loudness in all five); the long takes sit on slower, building tracks (70 to 78 BPM, the median 73) and the cut-up ones on faster flat ones (117 to 124 BPM, the median 117), as expected; the cuts are **not** beat-locked (the median distance to a strong onset is 183 ms, 40 % within 100 ms; one video is close to locked at 30 ms, with three cuts); loudness is -15.6 to -19.0 LUFS, music only, no sound effects. The first cut sits at the lift in two of the three videos that have cuts (the first video's first cut, at 2.1 s, comes a second before its lift). The tempo estimates are at half time for slow tracks (78 might be 156): the octave ambiguity is documented, and the profile uses the tempo only to classify long-take against cut-up tracks.

### The profile of the five, in words (`cfctl style show`)

> Creator style, from 5 reference videos (confidence 90 %). Pacing: long takes. 3.0 (0.0 to 5.0) cuts a video (7.1 (0.0 to 16.2) a minute), shots 1.9 (1.8 to 3.0) s long at the median (of the videos that have a cut), the longest 20.5 (7.4 to 43.8) s; 40 % of the videos have no cut at all. Hook: the first cut comes at 2.3 (2.1 to 2.5) s. Effects: none. Look: none grade, low-key footage. Sound: Quiet 2.9 s intro, then the music lifts (by 2.9 LU); cuts sit loosely near onsets (not beat-locked); energy builds to the end; no fade-out; music only at about -17 LUFS, no sound effects; tempo 70 to 124 BPM (about 73 for the long takes, 117 for the cut-up ones).

The thresholds that profile sets for an edit (`Profile.Thresholds`): the first shot at most 2.9 s, the first cut between 1.4 and 3.6 s, 0 to 6.5 cuts (at most 20 a minute), a shot of at least 7 s somewhere in the edit, a length of 13 to 40 s, no effects, no minimum effect rate, no building pace.

### Recommending a trend sound by structure (input to P5-02, docs only)

A finished video's sound is described by a structure we can compute from any audio we may process: the length of the quiet intro before the lift, the energy arc, the tempo and the loudness. P5-02 (sound metadata: tempo, beat grid, popular-snippet window) can match that structure instead of a genre guess: for a creator whose profile says "a 2 to 3 s intro that lifts, builds to the end, 70 to 80 BPM for long takes, 115 to 125 BPM for cut-up videos", the sound board should rank trending sounds whose snippet window has the same intro length (to within half a second of the first cut), an arc that builds, and a tempo in the right band for the edit's pace. The profile does not choose the sound: it gives P5-02 a similarity key (`intro_s`, `arc`, `tempo_bpm`, and `lufs` for the bed to sit under), and the editor already plans the hook to end where the music lifts. Nothing of this is built; the fields are in the profile so P5-02 can use them without a migration.

## 6. Storage and the upload route

- **Table `creator_style_profiles`** (migration `00021`, one past main's 00020): one row per workspace (`workspace_id` primary key, cascade on delete), `version`, `profile` jsonb (what the editor reads), `measures` jsonb (the per-reference measures, so a profile can be aggregated again and a reference removed, and the evidence is there to show), `references_count`, size checks (profile at most 256 kB, measures at most 1 MB). The grants are `cf_grant_app_dml`. The store tests run as the superuser and as the app role, and prove one workspace never sees another's row.
- **What is kept.** Numbers and the title text. **No media**: a reference video is read from a local file by the sandbox job and not stored, so "deleting the uploads removes the derived data" is true by construction, and `cfctl style clear` (and later the endpoint below) deletes the profile.
- **The upload route is not built**, by choice. The slice is dev-first: `cfctl style analyze --workspace ... --files a.mp4,b.mp4` (development only, like `cfctl project show`). An upload kind for reference videos would need a migration (the `assets.kind` check) and the OpenAPI spec, which this change does not touch. What the product route needs, to be built with the onboarding flow (P7-02): `POST /v1/style/references` (a reference upload, the normal upload flow with a `kind` of `reference` that is not shown in the library and expires), `POST /v1/style/analyze` (starts the job, returns a run id; the job is `Analyzer.AnalyzeFile` per reference, then `Save`), `GET /v1/style` (the profile and its evidence), `PATCH /v1/style` (the creator corrects a dimension, which sets a per-dimension override that survives re-aggregation) and `DELETE /v1/style`. The worker needs the same sandbox runner it already has for analysis.
- **Consent.** The references are the creator's own posted videos, uploaded by them. The wording for the consent and for the optional TikTok connect follows the P0-12 legal answer.

## 7. The editor follows it

- **Run context.** When the workspace has a profile, the run context ends with an introduction in our words and a `creator_style` `untrusted_data` block (the compact block plus the example titles). The runtime stores the context once per run, so the prompt cache is not disturbed; a workspace with no profile has the same context as before, and the cassettes are unchanged but for the prompt version.
- **`plan_edit`** ends its answer with a "Creator style" note computed from the profile (our words, no creator text: the length of the takes, where the first cut comes, the quiet intro and the lift, no beat grid, no effects, no labels, no look, no sound effects); the block itself is in the run context only. It adds a note to a plan that has effects for a creator who uses none, and it accepts `tempo_bpm 0` for a workout when the cuts are not on the beat.
- **Prompt `editor.v2.2`** (section "Following the creator's style", [changelog](../../apps/server/internal/agents/editor/prompts/CHANGELOG.md)): the creator's style wins over the playbook and the prompt's defaults; pacing (long takes), the hook (as long as the first cut, ending at the music's lift), effects, labels, look and sound effects (the sound-ready bed stays, it is not an effect), the title style, no beat snapping when the cuts are not on the beat (and cut on the drop when they are), the sound the creator adds, and the length. The defaults paragraphs say they are defaults.
- **A profile is a rule only when it is firm.** One or two references (confidence 0.2 and 0.36) are a hint: the block says so ("rests on only 2 reference videos"), and `check_edit`, `plan_edit` and `style_match` ignore it (`style_match` says why it is skipped). From three references (`styleprofile.MinReferences`) the profile sets thresholds. "Not on a beat grid" needs evidence too: it holds when at least three cuts were measured against the music and are loose, or when the creator's pacing is long takes (almost no cuts, so a grid means nothing); a montage creator whose cuts could not be measured is not called loose (`CutsMeasured`, `Profile.CutsOffBeat`).
- **`check_edit`** takes its thresholds from the profile (`applyStyle`) and has a new check, `style_match`:

| Check | Without a profile | With one |
|---|---|---|
| `hook_length` | at most 2.0 s | the 75th percentile of the creator's first cut plus 0.5 s, within 1.5 s of the median first cut, at least 2 s and at most 4 s |
| `hook_title` | a title by 0.5 s | not asked for when no reference has a title |
| `shot_pacing` | the median shot at most 3 to 6 s by type | a creator of long takes: up to the 75th percentile of their longest takes; any other: 1.5 times the 75th percentile of the median shot, at most 2.5 times its median (never under the default) |
| `pace_builds` | shots get shorter | does not apply to a creator of long takes or of no or light effects; a montage or busy-effects creator keeps it |
| `effect_density` | at least 0.15 effects a cut for a fast type | none when the creator uses none: any effect fails; no minimum for long takes or light effects; a montage or busy-effects creator keeps the minimum |
| `beat_sync` | a grid for workouts | skipped when the cuts are not on the beat |
| `duration` | the plan's length, or 15 to 45 s | 0.7 times the 25th percentile to 1.4 times the 75th percentile of the creator's lengths, within 0.4 and 2.5 times the median |
| `style_match` | skipped | per-dimension distance (below) |

**The thresholds come from robust statistics** (`Profile.Thresholds`, `thresholds.go`), not from the extremes: each bound is a quartile with room, capped by a multiple of the median and by hard limits (the hook at most 4 s, the first cut at most 4.5 s), so one odd reference (a single 44 s take in a montage creator's set, a video whose first cut comes at 30 s) moves a bound by a rank and never opens it. The shot-length statistics (`shot_median_s`, `shortest_shot_s`) come only from the references that have a cut; a creator of long takes is represented by `Pacing.Style` and the longest-take statistics. Every `Range` carries `p25` and `p75` next to the median, minimum and maximum, which are for the reader.

The `style_match` dimensions are cuts (count and pace; the fix says fewer cuts, fewer cuts or longer takes, or add cuts, by the bound that failed), long takes (a shot at least 75 % of the 25th percentile of the creator's longest takes, between 4 and 12 s), shot length (for a fast creator), the first cut, effects, title, labels, title case, captions, grade, sound effects and duration. Each carries the value, what was expected, and a one-line fix; the check fails when any is out of range and the report holds the score (the share in range). `cfctl eval editor` prints the distance.

The audio checks of P3-12 (`audio_level`, `audio_bed`, `audio_effects`) listen to our export (a bed at about -24 LUFS under the effects) and not to the creator's finished video, whose -15 to -19 LUFS is the trending sound they add afterwards; so they are not changed by the profile.

**A demonstration**, the checks of "Building Lats 101" as the creator cut it (a 2 s pose with the title, 1.3 s of the straps, 0.5 s walking in, an 8 s row, a 3 s front row, no effects) against a generic workout plan with a 124 BPM grid (`TestBuildingLatsPassesWithTheProfileAndFailsWithout`):

- with the profile: 8 of 8 applicable checks pass, `style_match` passes with 11 of 11 dimensions in range, `beat_sync` is skipped ("the creator's cuts sit loosely near the music's onsets, not on a grid"), `effect_density` passes ("no effects, as in the creator's videos");
- without it: `effect_density` fails ("0 effects in 4 cuts is flat; add a punch_in on a beat...") and `beat_sync` fails ("there is no beat grid; write one with snap_cuts_to_beats"), 2 failures that would send the editor off to add effects and a grid the creator does not use;
- a generic ten-shot workout edit with a punch-in, a whip and a speed ramp fails `style_match` on cuts (9 against 0 to 7), long takes (the longest shot 4.1 s against 7 s) and effects (4 against none), each with its fix.

## 8. Title text styles

Two styles join the registry (`internal/timeline/style`): `title_series` (Anton, a bold condensed uppercase title in cream `#F5F0DC`, no outline, a light shadow, letters of about 4 % of the frame height, retuned after the first live run: it was 6 % with a heavy shadow; for "BUILDING LATS 101") and `title_lowercase` (Inter Bold, a quiet lowercase line in cream, centred, no outline; "how to bake a croissant tricep"). The fonts are in `packages/media-tools/fonts` already (Anton, Inter and Montserrat ExtraBold, all OFL), so **no font was added**. The writer writes the case (the styles do not transform text). The editor uses `title_series` or `title_lowercase` from the profile's case, weight and condensed fields.

**The title as the creator has it (follow-up).** The first live run with a profile matched the structure of the creator's video and missed the title: two lines of heavy cream capitals at about 6 % with a dark drop shadow, against one line of pale yellow (#F2E9A0) at about 4 % with no shadow. Four things changed:

- **Text items carry overrides** (`fill`, `scale`, `shadow`, `align`; `timeline-v1` text items, the ops, the TypeScript types, a fixture and goldens). That also gives the creator's left-aligned two-line variant ("BRACHIORADIALIS BUILDER 101").
- **The vision read returns two more fields**, `text_height_pct` (the height of the capitals as a share of the frame height) and `shadow` (none, soft, strong), prompt `stylevision.v2`. The profile's text block gets `height_pct` (the median), `shadow` (the majority), `one_line_share`, and its colour is now the channel-wise median of the reads, not a majority of exact strings.
- **`Profile.TitleRecipe()`** turns the text profile into what the editor writes: the style from the case, the fill, a `scale` that brings the style's cap height (from the font's OS/2 `sCapHeight`, 3.95 % for `title_series`) to the creator's (omitted within 5 % of 1), the shadow, the alignment and the anchor `upper_third`, which is the top of the safe zone and so as far from the subject's head as the zone allows. The recipe is in the `creator_style` block and in the `plan_edit` note as `add_text` arguments, with "keep it to one line" when at least half of the creator's titles are one line. Prompt `editor.v2.3`.
- **`style_match` measures the title's look**: `title_colour` (CIE76 colour difference up to 12), `title_lines` (one line, when the creator's titles are), `title_size` (the capitals within 70 % to 135 % of the creator's, both as a share of a 9:16 frame's height, which is 1.78 times the short side, so the same scale passes on every format; the fix is a scale in the range the ops take) and `title_shadow` (not heavier than the creator's), each with a fix that names the value to set.

**Validation** (`cfctl render --profile preview --provider docker` of the live run's spec, `/tmp/cf-run7`): the style as it ran (two lines, 6 %, heavy shadow), the retuned style with no overrides (smaller, light shadow), the recipe (one line, #F2E9A0, no shadow, at the top of the safe zone) and the creator's own title, compared as crops. What is left: the creator's title sits about 6 % higher than ours can, because the top 12.5 % of a 9:16 frame is outside the safe zone (the platform's own interface), and a two-line title still runs into the head of a subject who is high in the frame, which is why the recipe asks for one line.

## 8b. Title placement and wording (follow-up)

The live run of `editor.v2.3` matched the title's colour, size, shadow and case and missed two things: a two-line title ("BACK DAY" / "BLUEPRINT") on top of the person's head, and words made up ("BLUEPRINT") where the creator's titles follow a template ("BUILDING LATS 101").

**Where the person is: the quick look carries it.** The quick look (P1-04b) already sends four frames of the clip to Haiku in one call, so the same call returns where the person is in each frame: a `subjects` list in the strict tool (`frame`, `present`, `x`, `y`, `w`, `h`, `head_top_y`, fractions of the frame; prompt `quicklook.v2`, `max_tokens` 700 in the routing). It was cleaner than a separate pass: no second call, no new frames, the same frames the editor's keyframe grid uses. Nothing needed a migration: the numbers are stored in the asset's `quick_look` jsonb detail, `subjects: {aspect, samples: [{at_ms, x, y, w, h, head_top_y}]}`, with the time of each frame the model saw.

- **Untrusted and bounded** (`internal/subject`): every number is finite and inside the frame (a hair of overrun is clamped), a box is at least 3 % of the frame in each direction, the head top lies inside the upper part of its box, a frame number that does not exist or comes twice is dropped, at most 8 samples are kept, the aspect is that of the picture the model saw (read from the JPEG, not from the model). An entry that fails is dropped, never repaired, and a clip with nothing left has no track; the labels are kept either way. The model's words never reach it: it is numbers only. The strict parse leaves out an incomplete entry, and a missing `subjects` member does not fail the quick look. The editor sees numbers only (the `subject:` line of a clip).
- **Existing assets**: `cfctl assets quicklook --workspace ... --subjects` runs the same call for the videos whose quick look has no boxes and stores the boxes and the frames while it **keeps the title, description and tags** (`Options.KeepLabels`): it merges only its own keys (`frames`, `subjects`, `subjects_read`, built key by key; `subjects` is null when the new answer has no boxes) into the quick-look detail (`MergeQuickLookDetail`), so the prompt version, the model, an edit made while the call ran stay, a failed run leaves the quick look as it was, and a clip with nobody in it is marked `subjects_read` and not asked again. `--asset` does it for one. The call is the quick look's own, `quicklook.v2`, so it costs what a quick look costs: from the price table, 7.9 k input tokens counted pessimistically and 120 output come to about $0.0008, the worst case with `max_tokens` 700 about $0.0011, under the $0.003 budget (`TestACallCostsLessThanTheBudget`); the live cost is for the founder's run to confirm. This slice has no model access, so the validation below uses boxes read by eye.
- **Reading it** (`ListAssetSubjects`, one query): the render job's stager (a database error here fails the attempt so that it is retried, like a transcript: a final is never rendered without boxes the preview had) puts the track of each asset it stages into `render.ResolvedAsset.Subject`; `check_edit` and the run context read the same index.

**Placement** (superseded by 8c for P6-06: a text is never left over the body) (`timeline.PlaceTextAvoiding`, shared by the renderer and the check, so the check says what the render does). For a text item the head regions of the clips on screen while it is shown (`subject.HeadRects`: the middle 60 % of the subject's width from the top of the head down a fifth of the box, mapped through the clip's reframe, zoom and focus to output pixels, and widened to the union of its place at the least and at the greatest camera zoom **in effect while the text is on screen** (the renderer's own maths, frame by frame, about the middle of the frame: a punch_in from its landing on, a push_in or pull_back by the zoom its easing has reached, a zoom_through only through the overlap of the transition at 1.45, a shake only while it lasts; the renderer multiplies the factors, so a punch_in in a zoom_through peaks at their product), so a title cleared above the head stays clear when the camera moves in under it, and a hook title that ends before a late zoom_through or punch_in is not pushed below the head by it; a shake adds what it can shift) are the obstacles. **The person is two regions** (`timeline.Subject`, made by `subject.Regions`): the *head* (head and shoulders, as above) and the *body* (the subject box from the top of the head down, arms included; a model's box starts a little above the hair, so what is within 3 % of the frame above the head is not part of it, and "above the body" is "above the head"; a raised hand or arm higher than that is, and the box top counts). The head is mandatory, the body soft. **What is tested is the ink of the text, not its layout box** (`Layout.InkBox`): the line box of a title in capitals is about twice as tall as its letters (Anton: a 161 px box for 77 px capitals), so half of the box is empty, and a test renders every style with libass, over texts with the tallest glyphs (brackets, `$ / | @ ^ ~ *`, ring and stacked accents), and checks that the ink is inside the ink box (and not much smaller). The rise above the capitals is a share of the cap height per class of line, measured with libass: 0.08 plain, 0.36 with a bracket or a bar, 0.36 with an accent, 0.66 with a ring or a stacked accent; a stacked accent in Inter rises above the line box itself, so the ink box is not clamped to it and the zone moves by the real offset in both directions. The order is: (1) **stay** where the anchor puts it when the ink is clear of the whole person; (2) **above the person**, at the title's own size and then at smaller ones down to 0.6 of its style's (at each size where it stands first, when that is clear), the letters a gap (1 % of the height) over the head; (3) **below all of the subject** (the lower part of the frame), at every size, when there is room inside the zone; (4) **only as the last resort over the body**: where it stands when that is clear of the head, else just under the head, and the result says `OverBody`. It is never put just below the head over the torso while a smaller title fits above it. When even the head cannot be cleared it stays, with `OverHead`. In the renderer: `text_moved` when it moved or shrank, `text_over_subject` for a title over the body ("is below the subject's head, over their body" or "off the subject's head but still covers their body") or on the head.

**The safe zone and the ink.** The documented top margin (research 3.4: 240 px, 12.5 %, from TikTok's In-Feed ad template, scaled) is about where text may be, so for a title moved above the person it is the *ink* that must stay inside it: the empty space of the line box over the letters (about a third of the box for capitals) may reach into the margin, and the placement carries the zone it was checked against (`TextPlacement.Zone`, which the renderer clamps with). The numbers themselves are unchanged. The creator's titles, whose letters start at about 13 % (250 px), are consistent with this and not with the layout box inside the zone. A relaxed margin of 11 % (211 px) was considered and **not** adopted: the documented sources do not confirm it (a search found 240 px from the ad template, 220 px from one checker, and lower figures, 150, 130 and 90 px, with no source for the organic feed), so 12.5 % stays.

`avoid_subject: false` on a text item (schema, `add_text`, `update_text`, TypeScript types) turns it off for that item.

**`check_edit`: `text_over_subject`.** For each text over a clip with a box: pass when none covers the person; pass with a line saying what the renderer does when it covers it and can be cleared (above the head at a smaller scale, below the body; with the head's position and the height of the clear band above it, in numbers); **fail on the head**, with a fix (one line, a lower scale, a shorter text; or "remove avoid_subject false"), when it cannot be cleared or the item asks not to be. A title that can only be kept off the head and ends over the body is a **borderline** note (a pass that says so), and a **failure on a pose or physique shot**: a clip whose title or tags have a whole word `pose`, `posing`, `physique`, `flex` or the like (`subject.ShowsOffTheBody`, set on the track by the loader from the asset's labels, which are compared and never passed on). Skipped without boxes, with the command that makes them.

**One line, enforced.** The earlier check asked for one line only when at least half the creator's titles were one line; the profile said "60 %" because that was the share of the videos with a title, and the one-line share of Gino's three titles is 1 of 3. A title wraps when it is long, so the profile now keeps `one_line_chars`: between the longest of the creator's titles that fitted one line and the shortest that wrapped (17 and 27 characters: 22). `style_match` holds a title of up to that many characters to one line (`title_lines`), and **when at least half of the creator's titles are one line, a longer title too, with the fix "shorten to N characters or fewer so it fits on one line"**; the other fixes remove a line break of the editor's own, or give the scale at which the text fits, and the recipe says "keep a title of up to 22 characters on one line (no line break in the text)" (when at least half the titles are one line, "shorten the text to N characters or fewer").

**Title templates.** `styleprofile.TitleKind` classifies a title by its shape: a series (`<TOPIC> <number>` with the topic in capitals), a lowercase line of three words or more, a question, a short label in capitals, other. The profile keeps each kind with its share, examples (the creator's words, untrusted: they are only in the untrusted `creator_style` block) and, for a series, the number it ends in. The block says "title templates: a series title, <TOPIC> <number> in capitals (67 % of the titles: 'BUILDING LATS 101', ...; always ends in 101); a lowercase witty line ...; prefer the most common template, with a topic taken from the footage" and the note (our words, no quotes) says to use the series format with the muscle or the exercise of the footage as the topic and not to invent a name. `style_match` has `title_pattern`, a **soft** dimension: a title in none of the creator's forms is listed as "borderline, not a failure" (`Dimension.Soft`, `Distance.Borderline`).

**Validation of the order** (`/tmp/cf-run11/hook.png`, ours on the left and the creator's on the right, `cfctl render --profile preview --provider docker --subjects` with the head top at 17 % as the live quick look read it): the first live run put "BACK DAY 101" under the head, over his upper back, because the layout box (161 px) did not fit the 86 px band; now the title is one line at 0.7 of its size above the head, its letters at 13 to 16 % of the frame, where his are, and the back is clear.

**Validation** (`/tmp/cf-run9/hook_compare.png`, rendered with `cfctl render --profile preview --provider docker --subjects`, boxes read by eye from the live run's hook frame, not by the model): (1) the title as v2.3 wrote it, two lines on the head; (2) the same title with the placement on: a two-line title cannot fit the clear band above the head, so it is moved below the head, over the back, with a `text_moved` warning, and `title_lines` would have asked for one line; (3) the title v2.4 should write, one line in the creator's template ("BUILDING BACK 101" for the footage), pale yellow, no shadow, at the top of the safe zone, clear of the head and not moved; (4) the creator's own hook frame. What is left: the creator's title is about 6 % of the frame higher than the safe zone allows, and our title ends 1 % of the frame above the head, where his has a hand's breadth.


## 8c. Never over the body (P6-06)

The founder: "the text is mostly over the body". The placement of 8b allowed a title over the body as the last resort; it no longer does. The person is now three regions (`timeline.Subject`, made by `subject.Regions`): the **head** (as before), the **torso** (the subject box below the head and shoulders, across its whole width, or the whole box when an arm is raised above the head, because what is beside the head is then an arm or the lift) and the body box that holds both. A text may cover neither the head nor the torso, on any frame it is on screen (the regions follow the camera, the living camera's push and drift included). The order:

1. where its anchor puts it, when that is clear of the head and the torso;
2. above the subject (the band above the head or a raised hand), at its size and then down to 0.6 of it;
3. **beside the head**, over the background next to it, its letters above the shoulder line, at every size;
4. below all of the subject, when that is background inside the zone.

When none fits at the text's own time, **`subject.FitText`** (shared by the renderer and the check) shows it in the longest part of its own time that has room (at least 0.8 s or half its length), else at the nearest other time of the same length that has room, and only as the very last resort leaves it out (`text_retimed` and `text_over_subject` warnings of the render). `text_over_subject` fails for any text set not to avoid the person that covers the torso, and for a hook title or a numbered chapter title (`1. ...`) that would have to move to another time or be dropped; a shortened or moved label is a note. Golden tests (`TestGinoLikeSubjectsKeepTheTorsoClear`) use subject boxes like the founder's: the head top at 15 to 20 % of the frame and the torso filling the middle.

## 3b. The camera, the shot lengths and the drop (P6-06)

The profile gets three measures, so that "no effects" no longer reads as "no motion":

- **`camera`**: package `camshift` finds, for frames one second apart, the sub-pixel shift of the whole thumbnail that best matches (the background dominates the match, so a person moving in front of a tripod reads as no shift); the median, in percent of the frame width a second, is the camera motion, and from 0.8 % a second a reference is hand-held. The verdict is the majority. The clip analysis summary gets the same measure (`camera static` or `camera hand-held` in the editor's analysis line), so `static_shot` can tell footage that moves by itself. Calibration: the founder's five tripod clips read 0; of the ten references eight read 0, the lifestyle video 4.5 % a second and one 1.2 %. The references are mostly fixed-camera too.
- **`pacing.shot_s`**: every shot length of the references that have a cut (median 1.7 s, quartiles 1.1 to 3.2 s for the ten), its 90th percentile (`pacing.shot_p90_s`) and the last shot of each of those references (`pacing.ending_s`): the pace targets of an edit (edit style v1, "The pace, the hook title and the song").
- **`audio.drop_s`**: the drop of the music (`music.FindDrop`, the same function the song analysis uses): 3.1 s at the median (1.9 to 7.3 s), in nine of the ten.

The compact block the editor reads says them, and that a static camera gets the living camera; a profile made before P6-06 has none of them and reads as before. Clip analyses made before it have no camera field; `cfctl assets analyze --all --force` makes them again.
## 8d. Room beside the head, legible sizes and new titles (P6-06 follow-up)

A live run put its hook title on a posing shot whose head reached 14 % of the frame once the living camera moved: about 30 px of band
above it, no title fits there, and the renderer moved the title to the next shot. The hook had no title. Three things change, all in
the placement that the renderer, `check_edit` and the coming motion-graphics overlay share (`timeline.PlaceTextAvoiding`,
`subject.FitText`; the signatures are unchanged, `Avoidance` gains `Lines` and `Tried`, `Fit` gains `OwnTime`).

- **Smaller, down to legible.** The sizes tried go under the spec's smallest scale (0.6) to the style's legibility floor
  (`style.FitFloor`): capitals of at least 2.2 % of the frame height and never under a scale of 0.45 (`title_series` stops at 0.56 on
  9:16). A placement-only scale is not an override a spec can ask for (`style.Shrunk`).
- **Wrapped and stacked beside the head.** Beside the head the text is wrapped to the width of the room on that side, in up to three
  lines of whole words (a word is never broken), at every size down to the floor, at its own height (the top band) when that is clear,
  else just above the shoulders. Above the head is tried first at every size, below the person last; only then is a text moved in time.
- **The head itself.** The quick look's box is the whole person, arms out included, and the head was taken as the middle 60 % of it:
  half the frame wide in a lat spread, so there was never room beside it. `quicklook.v3` also reads the head alone (`head_x`, `head_w`)
  and the shoulder line (`shoulder_y`); the head is then that box widened by 15 % of its width each side, and the torso starts at the
  shoulder line. All three are optional and bounded like the rest (inside the person's box, below the head top); a value out of bounds is
  dropped, not repaired. Boxes read before v3 keep the old head, and `cfctl assets quicklook --subjects` reads them again (the detail
  records `subjects_prompt`). The cost is the quick look's own (a few more output tokens).

When a hook or chapter title still has no place at its own time, `text_over_subject` fails with what was tried and the room found
("above the head (a band of 32 px), beside it (at most 221 x 265 px of background) ... down to the smallest legible scale 0.56") and a
fix in numbers (at most N characters a line in up to three lines, another shot where it fits, or the command that measures the head).
Golden tests: `TestAHookTitleOverAHighHeadStacksBesideItInTheHook` (boxes shaped like the live posing shot: the head at 14 % with the
camera, about 30 px above it, the wall beside it clear) and `TestWithoutAMeasuredHeadTheLongTitleCannotStayInTheHook`.

**New titles.** The block the editor reads no longer holds the creator's titles: an editor shown their titles as examples wrote a
title that was word for word one of them. The templates are forms to fill: `"<TOPIC> 101" in capitals (a topic of 2
words: the muscle, the exercise or the session of this video)`, with the word counts measured from the titles, never the words. The
profile keeps every title read (`text.titles`, untrusted, never shown) so that `title_copied` can fail a copy, compared without case or
spaces.

## 9. Security

- A reference video is untrusted media: it is only an input of the sandbox job (no network, resource limits, the same runner as the clip analysis), results come back as regular files in the output directory with size caps, the parsers refuse malformed or oversize prints, and nothing of the file is kept.
- The vision answer is untrusted text and is validated before it is stored (above); the stored title and the file names are shown to the model only inside the `creator_style` untrusted-data block, whose delimiter name is stripped from the text; our own note (`styleNote`) is computed from numbers.
- The model call is metered (ledger, spend hold, a run budget of $0.01); `cfctl style analyze` is development-only (refused unless `CF_ENV` is local, ci or bench, like `cfctl project show`).
- Tenancy: the table has the workspace key, queries take it, the store tests prove isolation and run as the app role.

## 10. Not built, and what comes next

- Evidence frames (the plan's wording asks for them; the numbers and the example titles are there, the frames are not stored). The upload route, the worker jobs, the endpoints and the creator's settings are built: section 11.
- Left-aligned titles; animation and colour of captions (no reference has captions).
- A live vision run on the five references (section 4) and a live editor run with the profile; the editor's prompt was checked with the cassettes and the unit tests only.
- Per-genre profiles (a creator may edit a talking video differently from a workout), speech detection for the audio profile, and the trend-sound matching of P5-02.

## 11. The style in the app (P6-05b)

The founder's request: "let AI set the style for Gino based on the example folder, or add an option in the profile to tweak a person's
settings, or still upload videos as a reference for styling". Built as one feature ([D-36](../05-decisions.md#d-36-the-creators-style-is-three-layers-and-the-creator-always-wins)):

**Reference uploads.** A creator uploads 3 to 10 of their own finished videos through the normal upload path (`POST /v1/assets` with
`purpose: style_reference`). The asset is a video like any other (`assets.purpose`, migration `00024_style_references`), but it is never
footage: the library does not list it (`GET /v1/assets?purpose=style_reference` does), a project refuses it (`422 assets_not_usable`),
and its ingest runs no quick look, transcript or clip analysis. Limits: 10 that are not failed per workspace (`409 too_many_references`,
counted under the workspace's upload lock), 1 GiB (`413`), a video (`415`), 3 minutes (a longer one ends `failed`, `too_long`). The plan's
storage quota (`PlanLimits.storage_gb`) is not enforced anywhere yet, so the references do not count against it.

**The jobs.** The transaction that marks a reference ready queues its row in `style_references` and enqueues `style_reference` (the
analysis queue, unique by its reference while it waits or runs): the reference's 720 px proxy (the original when it has none; the
frame rate is the original's) is downloaded into the worker's media directory, measured in the sandbox (`Analyzer.TempDir`, so the
sandbox writes inside its allowed root) and its title read with the `stylevision` call ($0.01 at most). The row holds the measures; a
reference that cannot be measured on its last attempt, whatever the step, is `failed` with a code (`analysis_failed`,
`no_video_stream`, `too_long`). Every reference that finishes, fails or is removed enqueues `style_profile` for the next 15 s slot
(`jobs.StyleProfileWindow`; five uploads make one job). While any live reference is queued or analysing the profile job snoozes
(`jobs.ErrStyleLearning`) and changes nothing, so the editor keeps the profile and summary it has; when they are all measured it
aggregates the newest 10 measured references whose asset is live (and the files of cfctl), writes **one** summary and stores the
profile with the ids it was made from. A profile already made from exactly those references, none measured again since, is left alone
(no model call); with no reference left it is removed, and when the ones left all failed it is kept. A row left queued by a job that
gave up (older than `store.StyleInFlightMaxAge`, 26 hours) holds nothing back. A failed summary call is retried; on the last attempt
the profile is kept with its previous summary.

**Learning again, bounded.** `POST /v1/style/relearn` takes the workspace's style lock, refuses while a reference is being measured
(`409 style_learning_in_progress`) and past the plan's passes for the UTC day (`style_relearns_per_day` in the price list, 2 on the
trial, 3 on creator, 5 on studio and pro; `429 too_many_relearns`), records the pass in `style_learn_passes` and queues every ready
reference. A pass therefore costs at most 10 title reads ($0.01 each) and one summary ($0.03), plus 10 sandbox passes over 720 px
proxies; none of it is charged in credits, and the workspace's spend backstop still applies. `DELETE /v1/assets/{id}` refuses a
style reference (`409 style_reference`): `DELETE /v1/style/references/{id}` removes it with its measures and makes the style again.

**The summary** (`stylesummary`, prompt `stylesummary.v1`, Haiku 5.5 in dev-cheap and dev-mid, Sonnet 5.5 in v1-start, `max_tokens`
800, a run budget of $0.03, one call a learning pass; from the price table about $0.0008 on Haiku and $0.015 on Sonnet, the worst case $0.021). It reads the
measures in our words (`Profile.SummaryFacts`: `Describe` plus a note that "effects" counts only effects applied to the picture; no file
names, no title texts) and up to three posters of the references, and calls a forced strict tool that returns one or two sentences
in the second person ("Handheld gym shots that follow you, music on every video ...") and a value for each setting. The answer is
untrusted: unknown or missing fields, a value outside its enum, a colour that is not a hex code, an empty summary or one over 320
characters, a link, a handle or words aimed at a reader make it `invalid_output` and the profile is stored without a new summary.

**Three layers** (`styleprofile.Resolve`): the vibe is this edit's, else the creator's, else the suggestion's, else lively.
Transitions are the creator's; else, when their videos measurably use transitions (`Transitions.Used` some), the suggestion (when it
is not off) or the measured level (subtle, lots from half the cuts); else off, at every vibe (D-37: no vibe asks for transitions).
Text animation and camera motion are the creator's, else (when a person chose the vibe) the vibe's, else the suggestion's (except an
"off" at a lively or hype vibe), else the vibe's. The title's colour, place and case are the creator's, else the suggestion's, else
measured. Music is the creator's, else the suggestion's, else "always" when the videos have music. Pace is the creator's, else the
suggestion's, else "like my videos". Every value carries its source (`you`, `suggested`, `measured`, `vibe`, `this_video`, `default`),
which the API returns and the app shows. `Style.Applied()` puts the creator's title colour and case on a copy of the profile, so the
title recipe and the checks follow them.

**The editor** (prompt `editor.v2.9`): the run context lists the effective settings in our words after the `creator_style` block (enums
and a hex colour, so outside the untrusted block), with where each came from; the block's effects line now reads "applied punch-ins,
flashes and speed ramps measured in the picture". `plan_edit` points out a plan's transitions or motions when the settings turn them
off. `check_edit`: `effect_density` fails a transition when transitions are off and a motion or ramp when camera motion is off, and
allows 1.5 effects a cut (not 0.5) at "lots"; `style_match` replaces the measured `effects` dimension with `transitions`,
`camera_motion` and `text_animation` from the settings (hard when the creator chose them, advice when they came from the vibe, the
suggestion or the default), and turns the pacing dimensions into advice at a pace other than "like my videos". Music "never" drops
the beat grid. A workspace with neither a profile nor settings, and a run without a vibe, have the context of before.

**API** (contract first, tag `style`, paid-only, any role reads, owners and editors change): `GET /v1/style` (the summary, the measures
in plain words, the settings, the effective settings with their sources, the references with their state and a poster, the overall
status `empty`, `learning` or `ready` and the progress), `PUT /v1/style` (replaces the settings), `DELETE /v1/style` (the references,
their measures, the profile and the settings), `POST /v1/style/relearn` (every reference measured again, then the profile),
`DELETE /v1/style/references/{id}`. `POST /v1/projects/{id}/edit` takes `vibe`. "Owners and editors" are the roles that change
assets and projects too (a Clerk member is an editor); viewers only read. Tenancy: the workspace comes from the token; every
query filters by it; an asset of another workspace is `404`.

**cfctl** (`cfctl style analyze|show|set|clear`) goes through the same code: the files are stored as references without an asset
(replacing the earlier files, or added with `--append`), `creatorstyle.Learn` makes the profile and the summary (`--no-summary`,
`--no-vision`), `show` prints the service's view, `set` the creator's settings, `clear` the app's reset.

**Not built:** the storage quota of the plan; trimming a long reference instead of refusing it; the creator editing the summary text.

## 12. Learning what was tuned by hand (P6-05c, profile version 2)

What the first creator's edits were tuned for by hand (a series title, numbered chapter titles, long holds at the end, angle switches,
no transitions) is now measured per creator and kept as data, so no creator's words or numbers live in code, prompts or constants
(they are in the test fixtures only). `ProfileVersion` is 2; a version 1 profile is still read (the new parts are then unknown and
the settings fall back to the defaults). Every measure is per reference (`Measures`) and aggregated:

| Measure | Per reference | In the profile | How |
|---|---|---|---|
| Title form | the title as read | `Text.Templates`: a template, its share, typical length and lines | a word that comes back in two titles or more stays, a counting number at the start and every other run of words are slots (`<N>`, `<TOPIC>`, `<GOAL>`): "<TOPIC> <N>", "<N> MOVES FOR <GOAL>" (`template.go`) |
| Chapter titles | `Text.Chapters`, read from a frame 0.6 s after up to 3 later cuts (`stylevision.v3`) | `ChapterShare` of the multi-shot videos read, `ChapterTemplates` ("<N>. <TOPIC> FOR <GOAL>") | the same template learning |
| Title entrance | `Text.Entrance` (pop, slide, fade, typewriter, none), from two frames 0.08 s and 0.3 s in | the majority | the vision read |
| Shot lengths | the shots | `Pacing.ShotS` with `p90` and P6-06b's `ShotP90S`, `Pacing.EndingS` (the last shot of the videos with a cut); "a long hold at the end" when it is 2.5 s or more and 1.5 times the median shot | |
| Transitions | `Transitions`: each cut hard, blend (a change spread over several frames), flash or zoom | `Transitions.Used` none, cut_only or some, with the kinds and the share | the scene scores and thumbnails around each cut (`cuts.go`); a proxy, not a reading of the effect picked |
| Camera | P6-06's `camshift` | `Camera.Level` static, subtle (a drift of 0.3 % of the frame a second or more) or handheld | |
| Cuts against the movement | `Movement`: per cut after a shot with reps, on a rep boundary (within 150 ms, or after the set), mid-rep, or inside a set (reps at the same pace on both sides) | the shares, known from 3 measured cuts | the clip analysis' rep detection run on the reference |

**What the rep detection allows, and what not.** It finds reps in a stretch of a few seconds of one set, so a cut after a long take
is measured and a cut between two short shots is not: on edits of 1 to 3 s shots the measure is "not measurable", and the profile says
so (`cuts against the movement: not measurable`). It does not know which exercise a shot shows, so an **angle switch within one exercise**
is a proxy: a cut with reps at the same pace (within 25 %) on both sides. A local check on four of the founder's reference videos (not
committed, no model call) measured plain cuts only, a long hold at the end (8.1 s at the median against 2.7 s shots) and a static camera,
and found the cuts not measurable against the movement: their shots around the cuts are too short for the rep detection.

**Settings.** Five more are overridable, with the same precedence (the creator, then the AI suggestion, then the measures, then the
default): `chapter_titles` (on, off), `title_template` (a form the creator types, validated: letters, digits, spaces, slots in capitals,
80 characters; it reaches the editor as an untrusted `title_form` block), `ending_hold` (long, short), `cut_timing` (full_reps, mid_rep)
and `angle_switches` (on, off). The summary call (`stylesummary.v2`, now `v3`, which proposes transitions only when the measures show them) suggests the four enums too and is asked to mention what stands
out ("long holds at the end", "numbered chapter titles", "cuts mid-rep"); it sees that the titles follow a form but not the form's words.

**The editor** (`editor.v2.9`): the creator_style block lists the title forms ("fill the slots from the footage; never copy a title of
theirs") and the chapter forms, never the creator's titles (P6-06b's `title_copied` fails a copy); the settings say each choice in our words;
`style_match` adds `ending_hold` and `title_form` (hard when the creator chose them); `rep_boundaries` does not apply when the cuts are
mid-rep and `chapter_titles` not when chapter titles are off. The prompt's examples that were one creator's are now slots.

**Chapter titles are not titles** (P6-07 follow-up). A numbered chapter title (a `chapter_card`, or a text or graphic whose words are
"1. ...") counts in `EditStats.Chapters`, never as the title or a label: the `title` dimension counts the one title (a title text item,
a `title_pop` or a `blur_in`), and a `chapter_titles` dimension follows the effective chapter-titles setting (on: the cards are expected,
the editor check `chapter_titles` counts them against the plan's chapters; off: there are none). It is hard when the creator chose the
setting. A live v2.9 run with a `title_pop` and two chapter cards failed "3 titles, expected one title" before this.

**The text read** shows the model up to 8 frames (5 of the hook, 3 after later cuts) with `max_tokens` 800; the worst case on Haiku 5.5
is about $0.002, under its $0.01 budget.

**With P6-06b's pace targets.** The creator's `pace` setting scales the pace targets of `internal/agents/editor/pace.go` after the vibe
(slower x1.25, like my videos x1, faster x0.8), in plan_edit and in check_edit; the title form the creator sets is the form those
targets' title instruction fills. The two measures of the ending are one: `Pacing.EndingS`.

## 13. All the text on screen, and two detector fixes (P6-05d)

**Two bugs of the live re-learn.** (1) A creator of plain cuts read as "transitions: subtle (measured)" from zooms on 8 % of the cuts:
a hand-held lifestyle reference and a camera push into a cut summed to a scale change near the cut. A cut is now a zoom only when the
picture scales on **both** sides of it (`zoomSideS` 0.35 s each, every step the same way, at least ln 1.03 a side), the frames at the cut
are blurred (sharpness at most 0.75 of the same shots further away) and the shots are quiet in the second before (a moving camera is
hand-held or a push). Transitions are the style only when `Evident`: on a fifth of the cuts or more, in two videos or more
(`TransitionsProfile.Videos`); fewer is plain cuts, and the summary prompt (`stylesummary.v4`) says so. Synthetic thumbnails test a
zoom-through, a push into a plain cut, a hand-held camera and a plain cut (`zoom_test.go`). (2) The summary suggested `as_typed` for titles
measured all in capitals, and the suggestion came before the measure. The measured case now comes before the suggestion in `Resolve`, and
the summary's suggestion is set to the measured case (`MeasuredTitleCase`).

**The text of every shot** (`stylevision.v4`). The one text read per reference now sees the five hook frames and one frame a little into
each shot (`ShotTimes`: up to 10, spread over the shots; a video of one or two takes gets a frame every few seconds), cut in two ffmpeg
passes (`media.MaxFrameBatch`). For each shot frame it reports the line, its place (top, upper_third, middle, lower_third), cap height,
colour, shadow, whether it lies over the person's body and whether it is dialogue, and the language of the text. Fifteen frames are about
4,000 input tokens: about $0.0005 a read on Haiku 5.5 (the live reads cost $0.001 each), so the $0.01 budget stays and `max_tokens` is
1,500. Lines are the creator's words: kept as untrusted data like the title, never shown to the editor.

**The pattern**, in code (`ClassifyText`): `dialogue` when two lines or more read as speech (quoted, or a question or an exclamation
the read flagged as dialogue: the flag alone is not enough, it marked first-person captions too); `story_captions` when a new line comes
on half of the shots or more (at least two, three when more than three shots were seen; numbered chapter titles do not count, the first
line does); `single_title` when there is a title or a line; `none`. The profile keeps the majority (`TextProfile.Pattern`), the look of
the pattern's lines (`LineLook`: place, size, case including sentence case, colour, shadow, the share over the body, words a line) and the
language.

**Settings** `text_pattern` (single_title, story_captions, dialogue, none) and `text_position` (top, upper_third, middle, lower_third,
auto): the creator's, else measured, else one title and auto. **Placement modes**: a text item takes `avoid` body (the default: the head
and the torso) or face (only the head and shoulders, `PlaceTextAvoiding` and `TextCovers` treat the head as the whole person).
`Style.FaceOnly`: a style told in lines whose lines sit in the middle of the frame, or whose own lines lie over the body half the time or
more, may put its lines on the chest; a title, and a title creator's text, keeps off the whole body (`text_over_subject` fails avoid face
for them).

**The editor** (`editor.v2.10`, `workout.v7`): the settings line names the pattern, the look, where the lines sit and the language;
"The text of the video" says how to write story captions (a mini-story about the footage, one line per shot, in the creator's language,
never a line of theirs) and dialogue. Checks for a style told in lines, and only for it: `story_captions`, `caption_readable` (1 s),
`text_over_face`.

**Verified** with `cfctl style analyze --no-store --workspace` (the reads metered to a local workspace; nothing stored), on a caption
creator's five videos and the first creator's ten (local only, never committed): see the PR.

**Changing one setting.** `PUT /v1/style` replaces all the creator's settings (a setting it leaves out is not set); the web app always
sends the complete set (`withMySettings`, tested in `pages/style.test.tsx`). `PATCH /v1/style` changes only the settings it is given and
clears the ones named in `clear`, in one transaction under the workspace's style lock; `cfctl style set` uses it (a live
`set --chapter-titles on` had reset every earlier choice), and takes `suggested` as a value or `--clear name,...` to give a setting back
to the suggestion, the measures or the default.

