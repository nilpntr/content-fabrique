# Design note: the creator style profile v1 (P6-05, as built)

> Status: **built, development-first slice** · 2026-10-17 · The implementation of [P6-05](../04-implementation-plan.md) up to the upload route: the analysis, the stored profile, the editor that follows it and the checks that hold an edit to it. It extends [edit style v1](edit-style-v1.md) (the genre playbooks are the generic craft; the profile is this creator's craft), [audio v1](audio-v1.md) (the ears), [agent-runtime v1](agent-runtime-v1.md) (untrusted data) and [spend control v1](spend-control-v1.md) (the one model call). Code: `internal/styleprofile`, `internal/media/stylestats.go`, `internal/store/creator_style.go`, `internal/agents/editor/style.go` and `check.go`, `cmd/cfctl/style.go`.

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

Two styles join the registry (`internal/timeline/style`): `title_series` (Anton, a bold condensed uppercase title in cream `#F5F0DC`, no outline, a soft shadow; for "BUILDING LATS 101") and `title_lowercase` (Inter Bold, a quiet lowercase line in cream, centred, no outline; "how to bake a croissant tricep"). The fonts are in `packages/media-tools/fonts` already (Anton, Inter and Montserrat ExtraBold, all OFL), so **no font was added**. The writer writes the case (the styles do not transform text). **Left alignment is not built:** one of the five videos sets a two-line title left-aligned, and the timeline text item has no `align` field; adding one is a schema change (`timeline-v1`, the ops, the TypeScript types) and is listed for a later slice. The editor uses `title_series` or `title_lowercase` from the profile's case, weight and condensed fields.

## 9. Security

- A reference video is untrusted media: it is only an input of the sandbox job (no network, resource limits, the same runner as the clip analysis), results come back as regular files in the output directory with size caps, the parsers refuse malformed or oversize prints, and nothing of the file is kept.
- The vision answer is untrusted text and is validated before it is stored (above); the stored title and the file names are shown to the model only inside the `creator_style` untrusted-data block, whose delimiter name is stripped from the text; our own note (`styleNote`) is computed from numbers.
- The model call is metered (ledger, spend hold, a run budget of $0.01); `cfctl style analyze` is development-only (refused unless `CF_ENV` is local, ci or bench, like `cfctl project show`).
- Tenancy: the table has the workspace key, queries take it, the store tests prove isolation and run as the app role.

## 10. Not built, and what comes next

- The upload route, the worker job and the endpoints in section 6; the creator's edit of a dimension; evidence frames (the plan's wording asks for them; the numbers and the example titles are there, the frames are not stored).
- Left-aligned titles; animation and colour of captions (no reference has captions).
- A live vision run on the five references (section 4) and a live editor run with the profile; the editor's prompt was checked with the cassettes and the unit tests only.
- Per-genre profiles (a creator may edit a talking video differently from a workout), speech detection for the audio profile, and the trend-sound matching of P5-02.
