# Design note: edit style v1, from "correct" to "viral"

> Status: **accepted** · 2026-10-17 · Written after the first live Opus 5.5 edit of real gym footage (Gino's back-day clips; $0.22 per run). The structure was right; the craft was rough. This note says what "good" means, which renderer features, analysis signals and editor behaviours get us there, and how we measure it. It extends [timeline-v1](timeline-v1.md), [agent-runtime-v1](agent-runtime-v1.md), [D-02](../05-decisions.md#d-02-edit-representation-declarative-timeline-json--deterministic-renderer), [D-03](../05-decisions.md#d-03-music-cue-only-by-default) and [D-26](../05-decisions.md#d-26-video-effects-are-presets).

## 1. What the first edit got wrong

| Symptom | Cause |
|---|---|
| Every cut was exactly 3.0 s | No sense of rhythm, no beat grid, no guidance on how long shots should be |
| Cuts landed mid-rep, and two "reps" were the lifter standing between sets | The editor saw 3–8 stills per 90 s clip, and had no rep or dead-time signal |
| Brightness jumped between angles | The look was the same preset everywhere; nothing evened out exposure |
| Plain hard cuts and small static labels | There were no motion presets, no transition styles, no SFX, and only basic title animation |
| The editor never really checked its work | No self-review step and no measurable quality bar |

## 2. Understand first, then pick a genre playbook ([D-28](../05-decisions.md#d-28-content-aware-editing-across-genres))

Every editor run starts by **understanding the footage**. It reads the quick-look titles and tags, the transcript, the per-clip analysis (motion, reps, holds, dead time, faces and subjects, exposure) and the brief. It then writes a **creative plan**, shown to the creator in the run feed:
- the content type;
- the story in one line;
- the hook;
- the arc;
- the ending;
- the playbook it will use.

| Playbook | What "good" means |
|---|---|
| **Talking / opinion** | The hook is the boldest line, often lifted from the middle. Jump cuts remove filler words, pauses and restarts. Punch-in zooms land on emphasis. Word-highlight captions are on by default. B-roll or text covers jumps when available. The speaker stays centred in 9:16 using face tracking. |
| **Unboxing / product** | The hook is the reveal moment, or the product at its best, shown first. Then the anticipation, the reveal, detail close-ups (texture, logo, features), the product in use, and a verdict. Speed ramps run through the boring parts of opening the box. Label callouts name the product and its key features. A satisfying sound is kept or an SFX added on the reveal. |
| **Workout / fitness** | Defined in §3. |
| **Vlog / lifestyle** | Follows the time of day, or a before and after. Cuts move quickly through transit and slow down for moments with emotion. Natural sound is kept, with music-led pacing. |
| **Tutorial / how-to** | The result comes first ("here's what you'll make"), then numbered steps with on-screen step labels. Every step is shown clearly, and the slow, repetitive parts are trimmed or sped up. |
| **General** | Hook, best moments in rising energy, loop ending. |

## 3. The fitness playbook (15–35 s)

- **The hook (0–2 s)** is the most striking frame first: a peak flex, the heaviest lockout, or a transformation. It gets a punch-in or speed ramp and a big title that lands on the first beat. Nothing slow happens before 1.5 s.
- **Rhythm.**
  - Cuts land on a beat grid, which comes from the trend sound or, when there is none, a default 120–128 BPM grid.
  - Shot lengths vary: 1–2 beats for hype moments and 2–4 beats for a full rep that is worth seeing.
  - The cuts speed up toward the end.
  - Never two equal-length shots in a row more than twice.
- **Movement and continuity.**
  - Reps are cut at the top or bottom of the movement, never in the middle.
  - Cut on motion (match cuts) where possible, and skip dead time completely.
  - Prefer the cleanest, sharpest, best-exposed reps.
- **Energy.**
  - Use punch-in zooms on the beat, short speed ramps (fast into slow at the peak contraction), a whip or zoom-through between exercises, and a flash or impact on the drop.
  - Use them sparingly: about one effect per 3–4 cuts.
- **Look.**
  - Every shot is evened out to a common exposure and contrast, then a single grade is applied ("gym moody": warm skin, a little contrast above the shadows, a soft vignette; it must not push low-key shadows down).
  - Nothing flickers between angles.
- **Text.**
  - One bold title for the hook, with kinetic animation (pop or slam, synced to the beat).
  - Exercise labels arrive with the exercise and are short, uppercase and high-contrast.
  - Optional word-highlight captions when there is speech.
  - Everything sits inside the safe zones.
- **Sound.**
  - The trend sound stays `cue_only` (D-03).
  - The preview plays the beat guide.
  - Generated SFX (eleven categories with variants, [P3-11](#sound-p3-11)) are rendered into exports. They are synthesised by us, so there is no licensing issue.
  - Source audio is ducked under an accent where it carries speech (the renderer does it), and muted where it does not.
  - The rules for density, sync and the arc are in [Sound (P3-11)](#sound-p3-11) below.
- **The ending** loops back to the hook (a held pose or the same frame), so the video replays smoothly.

## 4. What we build, in order

| # | Area | What | Task |
|---|---|---|---|
| 0 | Understand | Transcripts with word timings (P2-05, now urgent), speech structure (sentences, filler words, pauses), **face and subject detection for reframing** (MediaPipe, D-16), genre classification and the creative plan step | P2-05, new P2 task, editor v2 |
| 1 | See | Fix frame sampling; ffmpeg-only signals per clip (motion energy, exposure, sharpness, scene changes); shots; **reps, holds and dead time**; a `find_moments` tool; a per-clip analysis summary in the run context | `analysis-motion-reps` (P2-02/03/09 slices) |
| 2 | Render | **Motion presets** (punch-in, push-in, pull-back, shake), **speed ramps** (variable speed within one item), **transitions** (whip, zoom-through, flash, dip; existing crossfade), **auto exposure match plus grade presets**, **generated SFX** (an `sfx` audio kind: whoosh, impact, riser, synthesised in ffmpeg; redesigned in P3-11 as eleven categories with variants) | P3-10 (extended), P3-05. **Built (part A):** motion presets, speed ramps, `whip_left`, `whip_right`, `zoom_through`, `flash` (see section 6 of the [timeline note](timeline-v1.md)). `glitch` is not built (it has no cheap, deterministic form in ffmpeg). **Built (part B):** the `auto` and `gym_moody` looks, `look.match` with the bounded `exposure_ev` hint, and the generated `sfx` audio kind (whoosh, impact, riser, swish). The ops the editor uses are below |
| 3 | Rhythm | A default tempo grid when no sound is chosen (beat markers written into the spec); the editor uses `snap_cuts_to_beats`; the preview beat guide | P3-09 is done. **Built (editor v2):** the prompt's pacing and rhythm section and the order of the ops (section 7) |
| 4 | Editor v2 | The understand step and genre playbooks (prompt modules, versioned), the style guides above; a planning step (shot list with beats before ops); a **self-review loop** (after the preview, check the contact sheet plus frames at every cut against a checklist, then revise once); use of the new presets | P4-03 v2, P4-05 slice. **Built (editor v2, prompt `editor.v2.0`):** `plan_edit`, six playbooks, `check_edit`, the plan and check gates, the use of every preset in section 5 with restraint. **Not built:** a transcript read path for the talking playbook (the transcripts exist, the editor has no tool for them), face and subject detection, and a look at the frames at every cut (the objective checks replace the checklist for now) |
| 5 | Measure | Automatic edit checks: hook ≤ 2 s; shot-length spread; cuts on rep boundaries (±150 ms); no dead time; exposure spread across cuts; effect density; text in safe zones; duration in range; **sound** (loudness, true peak, first sound, silent gaps, the ending, sync to the cuts and the effects: `audiocheck.Check`, [P3-11](audio-v1.md), to be wired into `check_edit` in a follow-up). Plus `cfctl eval editor` over a small local scenario set (the founder's clips) that compares versions | P4-07 slice. **Built:** the checks (section 7) and `cfctl eval editor --workspace <wks> --project <prj> [--version N]`, which prints them for any stored version with no model calls. **Not built:** the scenario set and the comparison between versions or runs (run the command per version for now), and per-genre checks beyond the thresholds of section 7 (filler words, captions in sync, the reveal within 3 s) |

The checks are per genre (for example, talking: filler words removed, captions in sync, speaker framed; unboxing: reveal within the first 3 s). Each step gets a live Opus run on the same footage, with the edit checks from step 5 shown next to the previous run's.

## 5. Spec additions (all additive to timeline v1)

- **Video items:**
  - `motion: {preset: punch_in|push_in|pull_back|shake|none, at_ms?, strength?}`
  - `speed_ramp: [{at_ms, speed}]`, which replaces a single `speed` when present; the output duration is computed by integrating the speed over the ramp.
  - `look.preset` gains `auto` and `gym_moody`; `look.match: true` applies the per-shot exposure hint from the analysis.
- **Transitions:** `transition_in.type` gains `whip_left|whip_right|zoom_through|flash` (built); `glitch` was optional and is left out. `whip` stays as an alias of `whip_left`.
- **Audio items:** `kind: "sfx"` with `sfx: whoosh|swish|impact|riser|downlifter|sub_drop|pop|click|glitch|shimmer|sting`, an optional `variant` (`add_sfx` takes it too; `auto` picks one from the item id) and `at_ms`. These are generated by the renderer, so there's no asset and the export is `licensed`.
- **Markers:** the editor may write a beat grid (`kind: beat|downbeat`) from a chosen tempo. `snap_cuts_to_beats` reads them.

### Where the editor learns the new presets

The new craft features reach the editor agent as edit ops, whose descriptions in `ops.v1.schema.json` are its tool documentation (the editor prompt module that teaches *when* to use them is part of editor v2, step 4 above):

- `set_motion` (`punch_in` on a beat with `at_ms` relative to the clip, `push_in` or `pull_back` over a whole clip, `shake` on an impact), `set_speed_ramp` (fast into slow at the peak contraction), `set_transition` with `whip_left`, `whip_right`, `zoom_through` and `flash`: about one effect per 3 to 4 cuts.
- `set_look` with `gym_moody` for the single grade of a fitness edit, and with `auto` or `match: true` to even out exposure between angles. `exposure_ev` is written by the analysis, not guessed: a bounded nudge of -1 to 1 stops that must be computed from highlight or midtone percentiles, never from mean brightness (low-key gym footage is dark and correct).
- `add_sfx` (`whoosh` on a whip or a cut, `impact` or `sting` on a punch-in, the drop or the reveal, `riser` started its length before the moment it leads to (1.5 s by default), `swish` for small moves, `click` on the frame of an on-screen press, `pop` for an element appearing; the full list and the variants are in the op's description), moved with `move_item` and removed with `remove_sound`. The trend sound stays `cue_only`.

Every addition comes with schema fixtures, ops, validation rules (for example, a speed ramp must not exceed the source length, and a motion preset needs a long enough shot), compiler support and golden renders in all four formats.

## 6. Editor v2 as built (`internal/agents/editor`, prompt `editor.v2.5`; v2.6: "Music, energy and text off the body" below; v2.7: "The pace, the hook title and the song" below)

**The flow of a run.** Understand (`find_moments`, `view_clip`) → `plan_edit` → `edit_timeline` (in the order of section 7) → `render_preview` → `check_edit` → at most one revision (`edit_timeline`, `render_preview`, `check_edit`) → `finish`. The tools are in [agent-runtime-v1](agent-runtime-v1.md) section 5; the gates are the first `edit_timeline` of a run (refused without a plan) and `finish` (refused without a preview and a `check_edit` of the newest version the run made). Both gates keep their record in the worker and fall back to the run's events, so a run that resumes in another worker is not asked to plan or check again. The revision limit is guidance in the answer of `check_edit` ("this was the check after your revision pass: do not revise again"), not a refusal, so a run that cannot fix a check can still finish and say why; the run's budgets still bound it.

**The plan** (`plan_edit`, `plan.go`). The tool is strict. The content type and the playbook come from an enum (talking, unboxing_product, workout, vlog, tutorial, general), and the tool checks the bounds the strict schema cannot hold: story and hook at most 140 characters, arc at most 240, ending at most 140, a shot's `what` at most 80, `target_length_s` 5 to 120, `tempo_bpm` 0 (no beat grid) or 60 to 200, 1 to 16 shots of 1 to 64 beats each, an `asset_id` that is one of the project's clips, and no effect the build cannot make. It answers with the playbook of the chosen genre, the length of a beat and the shot list in milliseconds, and with notes when the shot list does not match the target length, has more than two equal shots in a row, or opens with a hook over 2 s. The plan is recorded as an artifact event, for the run feed to show:

```json
{"kind": "creative_plan", "tool_use_id": "toolu_...", "data": {
  "version": 1,
  "content_type": "workout", "playbook": "workout", "playbook_version": "workout.v2",
  "story": "...", "hook": "...", "arc": "...", "ending": "...",
  "target_length_s": 25, "tempo_bpm": 124, "pace": "building",
  "beat_ms": 484, "estimated_length_ms": 24200,
  "shots": [{"role": "hook", "what": "...", "asset_id": "ast_...", "beats": 3, "effect": "punch_in", "length_ms": 1452}],
  "prompt_version": "editor.v2.5"}}
```

`data.version` is the shape's version (a reader checks it first). A later `plan_edit` writes another event; the last one is the plan. `role` is one of hook, build, peak, detail, step, reveal, payoff, broll, ending; `pace` is steady, building or fast; `effect` is none, punch_in, push_in, pull_back, shake, speed_ramp, whip, zoom_through or flash. `asset_id` and `effect` may be absent (`effect` is `none` when left out).

**The playbooks** (`prompts/playbooks/<name>.v1.md`) hold the content of sections 2 and 3, one file per genre, each with its own version. **Prompt-cache placement:** the system prompt and the tool list are the first cache breakpoint and are the same for every project and run of a build, so they are shared. A playbook is not in it: the model chooses the genre after it has seen the footage, and the playbook arrives as the result of `plan_edit`, so it is part of the conversation (cached from the next call by the rolling breakpoint). Putting the likely genre's playbook in the prefix would split the shared entry into six, each warm for a fraction of the runs; putting all six there would make every call read five that it does not use. The run context (the second breakpoint) carries at most a one-line guess of the content type, made from the brief, the titles and the analysis (`LikelyContentType`); it is fixed for the run, so a resume sends the same prefix.

**Features.** The prompt and the playbooks are templates over a list of features computed from the embedded op schema (`features.go`: `op:<name>`, `transition:<type>`, `motion:<preset>`, `look:<preset>`, and `sfx` and `looks`). A section about an effect, a look or a sound effect is cut out unless the schema has it, and a test checks that every op the text names exists in `ops.Names` and that a build without the looks and sound effects is not taught them. Looks (`auto`, `gym_moody`) and `add_sfx` are on in this build. The prompt teaches one grade for all clips (`gym_moody` for workouts), whooshes on whips and zoom-throughs, an impact on the drop, a riser into a big moment, and tells the editor never to pass `exposure_ev` or `match`: the editor has no measured hint to give, and a hint made from how dark a clip looks would wash out low-key footage.

## 7. The checks (`check_edit`, `check.go`) and the order of the ops

Every number is computed in Go from the stored timeline version and the stored clip analysis. A check passes, fails with a one-line reason that says what to change, or does not apply. The result is the text the editor reads and an artifact event for the run feed:

```json
{"kind": "edit_check", "tool_use_id": "toolu_...", "data": {
  "record_version": 1, "version": 3, "content_type": "workout", "target_length_s": 25,
  "checks": [{"id": "hook_length", "status": "pass", "detail": "the first shot is 1.47 s long (at most 2.00 s)"}],
  "metrics": {"duration_ms": 12350, "shots": 6, "shot_min_ms": 1470, "shot_median_ms": 1930, "shot_max_ms": 3150, "longest_equal_run": 1,
              "rep_edges_on": 6, "rep_edges_total": 6, "dead_share": 0.02, "effects": 2, "ramps": 0, "effects_per_cut": 0.4,
              "cuts_on_beat": 5, "cuts": 5, "brightness_max_jump": 0.07, "brightness_range": 0.12,
              "audio_profile": "sound_ready", "audio_lufs": -21, "audio_bed_lufs": -24.2, "audio_source": "stem", "audio_ms": 310},
  "passed": 13, "failed": 1, "skipped": 1, "nth": 1}}
```

| Check | Passes when | Notes |
|---|---|---|
| `hook_length` | the first shot is at most 2 s | |
| `hook_title` | a text starts by 0.5 s, or captions are enabled | |
| `shot_pacing` | the median shot is at most 3 s (workout), 3.5 s (unboxing, general), 4 s (vlog), 5 s (tutorial) or 6 s (talking) | the type comes from the plan; without a plan it is general |
| `shot_variety` | never more than two shots in a row within 8 % (or 100 ms) of each other, and not all within a 12 % spread (5 shots or more) | |
| `pace_builds` | the second half's median shot is at most 15 % over the first half's, the last shot left out | workout, unboxing, vlog, general, 6 shots or more |
| `rep_boundaries` | at least 85 % of the shot edges on clips with reps are within the tolerance of a rep start, a rep end or its turning point, or inside a hold | the turning point is the analysis's measured one (`peak_ms`; the midpoint only when there is none); the tolerance is 150 ms at speed 1, more at a higher speed and on a slow rep (v2.7, below) |
| `dead_time` | at most 5 % of the screen time is dead time | counted in output time, from the dead moments |
| `effect_density` | between 0.15 and 0.5 effects per cut (the lower bound for workout, unboxing, vlog and general with 4 cuts or more), and at most 4 speed ramps | an effect is a motion preset, a transition that is not a cut, or a speed ramp |
| `text_safe_zone` | the version has no `unsafe_text` or `text_overlaps_captions` issue | the validator already refuses `unsafe_text` for a stored version, so this mostly reports |
| `duration` | 15 to 45 s, or within a quarter of the plan's target | |
| `brightness_jump` | no cut jumps more than 0.25 in the p90 luma of the lit part of the picture, or by 1.8 times with at least 0.10 | measured on the **rendered preview** (below), not the source: the exposure of each shot is the median, over the samples of the middle half of its time on screen, of the 90th percentile luma of the frame (`analysis.Series.ExposureBetween`): not the mean, so low-key footage is not penalised. In low-key footage the numbers are small, hence the ratio: the founder's posing clip (p90 0.37) is 1.9 times the gym clips (0.20) and only 0.17 apart. Not applicable without a preview |
| `crushed` | every shot's rendered p90 luma keeps at least 70 % of its source's p90 (allowing for the exposure hint the clip carries), and likewise its mean, and none renders near black (p90 under 0.04) | measured on the rendered preview against the analysis of the part of the clip the shot plays. Not applicable without a preview |
| `beat_sync` | at least 70 % of the cuts are within 2 frames of a beat marker | a workout, unboxing, vlog or general video with no markers fails; talking and tutorial skip it |
| `style_match` | the workspace has a creator style profile and every dimension of the edit is in the creator's range: cuts, long takes (or shot length), first cut, effects, title, labels, title case, captions, grade, sound effects, duration | skipped without a profile; see "Creator style (P6-05)" below. The report also holds the distance per dimension |
| `audio_level` | the ears' level checks of the preview's sound pass: has a sound track, the integrated loudness in the profile's window (sound-ready: -19 to -15 LUFS; programme: the web's -16 to -9), true peak, no clipping, DC or imbalance | measured on the **preview's audio stem** (the mix without the beat guide), or the video's own sound when there is no guide; `skip` without a preview (P3-12, [audio v1](audio-v1.md) section 6) |
| `audio_bed` | sound-ready profile only: the bed is there (sound over -45 LUFS at least 90 % of the time), has no gap over 0.5 s and is steady (the quietest tenth within 6 LU of the median) | the fix names `set_clip_audio` mode `bed` or an effect on a clip with no sound |
| `audio_effects` | the effects land on their frame (hard attacks within a frame, whooshes within 100 ms), every accent peaks at least 4 LU over the bed and within 6 LU of a -14 LUFS trend sound, and the density is at most 4 per 10 s | an estimate close to its bar is reported as "borderline" and passes |

`beat_sync` was not in the list of step 5; it is the check for step 3 (the editor writes the grid, so it should be measured).

**The order of the ops.** `snap_cuts_to_beats` cannot move a cut next to a clip with a speed ramp (it leaves it and warns `cut_not_snapped`), and a speed change, a transition or a ramp changes a shot's length and so moves every later cut. So the prompt says: (1) the clips, trims and reframes, and in the same batch everything that changes a length (`set_speed`, `set_transition`, `set_speed_ramp`, tuning a ramped shot to whole beats with `trim_item`); (2) `snap_cuts_to_beats` once, with `write_markers` and a small `max_shift_ms` (80), because a snap moves the edges of both its shots and would otherwise pull them off their rep boundaries (a second snap moves them again: use 60 or less); (3) what does not change timing: `set_motion`, texts, `add_sfx`, captions and clip audio, and last the look. To make rep-based shots whole beats without moving their edges, set the shot's speed to `(out_ms - in_ms) / (beats x beat_ms)` (a 3000 ms rep as 6 beats at 124 BPM is speed 1.03).

**Exposure in the analysis line.** The stored summary's `exposure_gain` and key brightness are mean-based and called the founder's clips dark, with a gain of x2.5 to match. The editor's analysis line is now computed at read time from the stored `luma` and `high` series (`ListAssetExposureSeries`), with no change to the stored format and no version bump: "low-key (p90 luma 0.18)" for a dark picture with a lit part, "underexposed (p90 luma 0.02)" only when even the lit part is near black, "bright" at a p90 over 0.92 with a mean over 0.65, nothing otherwise, and never a gain. The thresholds come from the founder's clips, which measure a p90 of 0.10 to 0.33 and a mean of 0.00 to 0.07 and render correctly, so underexposed starts at a p90 of 0.06. The shot and moment descriptions stored by the analysis ("dark", "well lit") and the exposure score of a rep or hold still come from the mean-based key brightness; fixing them needs a new analysis version and a re-run (`cfctl assets analyze --all`), and the prompt tells the editor to go by the clip's line.

**The rendered checks.** The source analysis says how bright the footage is, not how bright the render is, and a look can change the picture a lot. The first live Opus run of editor v2 put `gym_moody` on every clip of the low-key gym footage: the grade (a toe from 0.12 to 0.07, `eq` contrast 1.08 and a vignette) crushed it to near black (a rendered mean of 5 of 255 for the lat pulldown against 17 for the same shot ungraded), and a check that read only the source passed it. So `check_edit` also measures the preview render of the version: `RenderMeasurer` (`rendered.go`) downloads the preview MP4, runs one ffmpeg pass in the sandbox (`media.LumaStatsArgs`: `fps=5,signalstats`, writing the mean luma YAVG and the 90th percentile YHIGH of each sampled frame) and returns them as an `analysis.Series`, so the same `ExposureBetween` serves the source and the render. A shot is judged on the middle half of its time on screen (about three samples for a 1.5 s shot), away from its transitions. The thresholds were calibrated on the founder's two renders of the same footage: ungraded, every shot's rendered p90 luma is 0.18 to 0.37 (YAVG 0.07 to 0.13 normalised) and the graded render keeps 0.24 to 0.40 of it per shot (p90 0.05 to 0.15, YAVG 0.00 to 0.02); the ungraded render keeps 97 % or more, the graded 25 % or less. `check_edit` is refused until the version has been previewed (when a renderer is wired), and a version whose render cannot be read leaves both checks "not applicable" with a note, never a pass. The retuned `gym_moody` is pinned by a render test: on low-key frames its median, 90th percentile and mean stay within 12 % of the ungraded render.

**Brightness matching** (`match_exposure`, `matchexposure.go`). The editor is told never to invent an `exposure_ev`, and a clip much brighter than the others (the posing clip among gym clips) is a visible jump that no look fixes. The tool computes the hints in Go: the p90 luma of each clip's used range (a percentile, never the mean), the brightness most of the timeline has (the length-weighted median of the shots) as the target, and for each clip brighter than it `ev = log2(p90 / target)`, bounded to the 0.7 stops the render applies, nothing under the 0.15 it ignores, set with `set_look` (the clip keeps its look, `match` on). Only brighter clips are darkened; a darker clip is never lifted, so low-key footage is not washed out. The render scales the luma by 2^-ev below the middle of the range and keeps the black and white points (timeline-v1, "Looks and exposure"), so for a low-key shot, which lies wholly below that point, the p90 scales by exactly the gain and the p90 is the right number to match; a shot with its p90 above the middle moves less than the gain says, and the next `check_edit` measures the render. A call that has nothing to change says so; a hint that is already right is not set again, and a stale one is cleared.

**Shot lengths** are what is on screen: from where a shot starts to where the next one starts, so the overlap of a transition into the next shot is not counted (a 1.95 s hook with a 250 ms whip out of it is still a 1.95 s hook), and the last shot runs to the end of the video. Dead time is mapped through the clip's own time, ramps included, instead of scaled linearly.

**Budget and graceful ends.** `render_preview` and `check_edit` end their answers with what is left of the run's tool calls and time (`RunContext.Left`), with a warning when it is little, and a failing first check near a limit says not to start a revision. When a limit does come, `Agent.OnLimit` lets the editor end the run as succeeded instead of failed with "budget": if the run made a version and previewed one (or has no renderer), the output is the one `finish` writes (`implicit_finish`, with the limit), and its note for the creator says what was left undone (a last change that was not previewed, checks that were not run, the checks that still failed). A run that never previewed fails as before. The preview is an artifact event too (`{"kind": "preview", "data": {"version": N}}`), so a worker that takes a run over does not ask for a second preview.

**Measuring.** `cfctl eval editor --workspace <wks_...> --project <prj_...> [--version N] [--json]` runs the same checks over a stored version (the current one by default), with the plan of the editor run that made it when the run wrote one. With object storage (`R2_*`) and the media sandbox configured it also measures the version's preview render, so `crushed` and `brightness_jump` apply; without them it says so. It costs nothing and writes nothing. (`--workspace` is required, as for the other `cfctl` commands: every store query is scoped to a workspace.)

## Sound-ready exports (P3-12)

> Proposed by the director ([D-33](../05-decisions.md#d-33-exports-are-sound-ready)); the founder confirms. Built in `editor.v2.1`.

The export has no music and no trend sound, and the creator adds a trending sound in TikTok over it. So it must not be silent without that sound and must not fight it:

- **No speech:** the clips' own sound is a **low bed** (`set_clip_audio` mode `bed`, once, without `id`: about -24 LUFS, evened out across clips, short fades at the cuts, never normalised up to the programme), and the generated effects go on top.
- **Speech** (the brief or description says so, or the playbook is talking): that clip is `full`, the programme at its own level, ducked under effects; the other clips stay a bed. `full` is never for ambience.
- **A trend sound stays a cue**: never exported; the preview keeps the beat guide, and the edit is still cut to the grid so it matches what the creator adds.
- **Levels:** the bed is about -24 LUFS short-term and nothing is normalised to a programme target; the effects peak around -12 to -10 LUFS (within 6 LU of a -14 LUFS trend sound, which dominates the bed); the export integrates at about -21 to -19 LUFS for few effects. The editor leaves `gain_db` alone unless `check_edit` says an effect is lost in the bed.
- **The playbooks** (all at `v2`): workout keeps the gym as a bed, a whoosh on each whip, a sub_drop with the impact on the drop, a riser into the hook and a sting on the loop; unboxing keeps the reveal's own sound as `full` and the rest a bed; talking is `full` on speech and a bed on cutaways; general, vlog and tutorial use bed and full instead of `use_source`.
- **The checks** `audio_level`, `audio_bed` and `audio_effects` (section 7) listen to the preview and say what to change: a missing bed, an effect lost in the bed (a few dB or a heavier variant), a bed that is not at about -24 LUFS or an export outside -24 to -16 LUFS.

## Sound (P3-11)

> The rules below come from the measured profile of seven professional launch films and the practice notes of **earshot** ([github.com/Leonxlnx/earshot](https://github.com/Leonxlnx/earshot), MIT licence, Copyright (c) 2026 Leon Lin; its `docs/launch-video-sound.md` and `docs/motion-sfx-techniques.md`). "Measured" means measured by earshot on those seven films (small sample: what pros did here, not laws); "practice" is established professional practice it cites. We took numbers and ideas, not code. The sound design and the ears that check it are in [audio v1](audio-v1.md).

- **Density: restraint.** More sounds is not better. The films have a dense groove (2.5 to 8.2 onsets a second, measured) with only a handful of moments that jump out (1.2 to 1.7 salient hits a second). Sound the moments that carry meaning, the reveal, the logo, the key UI action and the section change; small motion rides on the music. About **one effect per 3 to 4 cuts, at most 4 per 10 s** (our estimate), and no hover or micro-motion sounds. A cluttered effect layer on busy music turns into noise.
- **Sync: on the frame, not on every cut.** An effect lands on its frame: a **whoosh** peaks on the cut (or on the fastest frame of the move) and starts 0.2 to 0.6 s before it, so put its `at_ms` that long before the cut and match its length to the move (`fast` for a quick whip, `heavy` for a big one); a **click** is on the frame the cursor presses, not when the UI reacts; an **impact** or **sting** is exactly on the reveal frame. A sound a frame or two off breaks the illusion: let it lead by up to a frame, never lag by two. A hit on every cut is **not** what the pros do: sound on visual events is no better than chance in most of the references (81 % of cuts have an onset within 100 ms, but 86 % do by chance, measured), because dense music covers the cuts. Deliberate accents go on the moments that matter. With a sparse sound track (no dense music) every cut needs some sound.
- **The arc: open strong, hold the body, land the ending.** Sound from frame 0, no silent cold open (the first sound within 0.04 to 0.48 s, median 0.13 s, measured). A steady body: the loudest 3 s sit only 1.1 to 3.5 LU above the programme (measured), so do not make the viewer wait for a climax. Land the ending at body level with a short fade or a sting finishing within about 0.5 s of the last frame; avoid a long fade-out and dead air (measured: a median 0.04 s of silence after the sound). Shape the energy with arrangement (layers in and out, a stop-down before the reveal), not with big level changes.
- **The big reveal.** A riser, then a stop-down (0.1 to 0.5 s of near silence, practice; 4 of 7 films drop the sound 10 dB or more for 0.15 to 1.5 s, measured), then an **impact with a sub drop** on the reveal frame. Once or twice per film, not on every scene. Reserve the longest, widest whoosh for the biggest move.
- **Loudness.** (A programme; an export with no speech is a bed and effects, see "Sound-ready exports" above.) Master at about **-14 LUFS integrated, true peak at most -1 dBTP** (practice; the renderer does this). Up to about -10 for a punchy social cut. The effects are mixed after the normalisation, at a level matched to their category, with a -1.5 dBFS ceiling.
- **Voice-over.** Duck the music 3 to 6 dB under speech and keep effects out of its way: never put a big hit on top of a key word (practice). The renderer ducks the programme a few dB under an accent that overlaps speech from a clip's own sound.
- **UI foley.** One click per on-screen click, short, bright (2 to 6 kHz) and well under the hits (about 10 to 15 dB under the music's peaks, practice); notifications short and soft.

The ears (`audiocheck.Check`, a model-free report on the rendered video) check these: first sound, time to body level, silent gaps, dead air, the ending level, the share of cuts and of planned effects with an onset on their frame (with the chance level next to it), effects per 10 s, the longest stretch with nothing new, and clipping. Each target in the report names its source, and the ones that are our estimates say so.

## Creator style (P6-05)

> Built in `editor.v2.2`. The full design is [style-profile-v1](style-profile-v1.md); this is how it changes the editor.

Sections 2 and 3 are the generic craft of a genre. A creator who shows us their own finished videos has a craft of their own, and where the two differ the creator's wins. The first creator we measured (five gym videos) lets takes run for 8 to 44 s, cuts 0 to 5 times a video, uses no effects, no grade and no exercise labels, puts one title in cream capitals in the upper third, and posts to a trending sound with a quiet intro of 2 to 3 s that lifts and builds to the end. Every default in the sections above is wrong for that creator, and the playbook said so nowhere.

- **A profile per workspace** (medians, ranges and a confidence over 1 to 10 reference videos) is measured with ffmpeg and one Haiku read of the title, stored in `creator_style_profiles`, and read by the editor.
- **The run context** carries a compact `creator_style` block as untrusted data; `plan_edit` ends with a note computed from the profile; the prompt has a section "Following the creator's style" that puts the creator's style over the playbook and over the prompt's own pacing, effect, hook and beat defaults. A workspace with no profile gets the same prompt behaviour as before.
- **`check_edit` takes its thresholds from the profile and has a `style_match` check** (the table in section 7 gives the checks that change): the hook may run to the creator's first cut, the median shot may be as long as the creator's takes, effects are none for a creator who has none, a beat grid is not demanded when the creator's cuts are not on the beat, the duration is the creator's range. `style_match` reports a distance per dimension with a one-line fix. `cfctl eval editor` prints it.
- **Two title styles** for the creator's titles: `title_series` (bold condensed uppercase, cream, a light shadow, about 4 % of the frame height) and `title_lowercase` (a quiet lowercase line), and per-item text overrides (`fill`, `scale`, `shadow`, `align`) that bring either to the creator's own colour, size, shadow and alignment. The profile's title recipe says which (prompt `editor.v2.5`), and `style_match` measures the title's colour, lines, size and shadow.
- **Sound.** The profile's audio block describes the trending sound the creator puts on after our export (the quiet intro and the lift, the energy arc, the tempo, the loudness, whether the cuts sit on the beat, music only or with effects). The export itself stays sound-ready (P3-12); the profile tells the editor where the hook ends (at the lift), whether to snap cuts to a beat grid and whether to add sound effects, and it is the similarity key for recommending a trend sound in P5-02.

## Music, energy and text off the body (P6-06)

> Built in `editor.v2.6` with the workout playbook `workout.v3`. The founder's verdict on the edits of v2.5: "static/stiff, not playful/vibey like the examples", "all hard cuts, no visual effects on entering the text", "the text is mostly over the body" and "no gym sound under it, just the music". A live run at max effort ($1.01) made the same static three-long-take edit as one at medium ($0.38): the tools, the playbook and the style rules were the limit, not the thinking.

**What the references measure** (the ten in the founder's folder; local files only, nothing of them is committed). Music in every one, loud (-12.5 to -21 LUFS), with a drop 1.9 to 7.3 s in (median 3.1 s, nine of ten). Shots of 1.1 to 3.2 s at the quartiles (median 1.7 s) in the videos that cut. The fast lifestyle video cuts six times in its first 7 s, each within 1 to 100 ms of an onset; the multi-exercise one is a listicle (a hook title, then "1. ...", "2. ...", "3. ..." per exercise, the angle switching every 2 to 5 s inside a chapter). Titles sit in the top band (8 to 25 % from the top), never on the torso. **The camera**: measured with `camshift` (the global shift of the picture a second apart), eight of the ten read 0 % of the frame a second, like the founder's tripod clips, and only the lifestyle video moves clearly (4.5 % a second). So "his clips are hand-held, ours are on a tripod" is not what makes the difference: the references feel alive because they ride loud music, cut on it, switch angles and move their text. The fix is the same either way: a living camera, cuts and transitions on the music, text that enters and leaves.

**The vibe** (`calm`, `lively`, `hype`; `lively` by default) is an input of the edit: `StartEditRequest.vibe` in the API contract, `cfctl agent run --vibe`, stored in the run's input, read into the opening message and the plan (`Plan.Vibe`), and used by `check_edit`. The creator's style profile still decides the look (colours, fonts, the title's look and place) and the rough shot length; the vibe decides the energy. A profile that measured "no effects" now means no gimmicks (speed ramps, shakes, sound effects over a song) at lively and hype: transitions, text entrances and the living camera are the vibe's. Calm stays close to the creator's restraint (hard cuts, one transition on the drop at most), but the camera still breathes.

**The living camera** (`motion` preset `handheld`, [timeline v1](timeline-v1.md#2-the-spec)): a slow push from 103 % to about 108 %, a drift across the room the push leaves that alternates direction from clip to clip, a slow sway, and a 6 % punch-in at `at_ms` (the drop or a strong beat). The renderer limits the push so that it never crops the top of the subject or the sides of the head. `set_motion` without an id sets every clip. It is camera motion, not an effect: `effect_density` and the style's effects dimension do not count it.

**Transitions.** At lively the cut into the drop and the cuts on strong beats or phrase starts are `whip_left`, `whip_right`, `zoom_through`, `punch` (new: a fast scale of 1.0 to 1.15 into the cut) or `flash`, 250 to 400 ms, never the same three in a row; cuts on ordinary beats stay hard. At hype at least every fourth cut. The whips, `zoom_through` and `punch` ride a **motion-blur trail**: three blurs over nested windows (the whole transition, its middle half, its middle frames), directional and horizontal for a whip (`dblur`), Gaussian for the zooms, so the blur peaks on the frame where the picture switches and the frames before and after are sharp (`TestTheBlurTrailPeaksAtTheCutAndLeavesTheNeighboursSharp` measures it on rendered frames). The switch is in the middle of a transition, so the checks count a transition on the beat when its start or its middle is.

**Text.** Every text item has an entrance (`animation`: the new `pop`, which scales to 112 % and settles, `slide_up` or `fade`) and an exit (the new `exit`: `pop_out`, `slide_down` or `fade`). A listicle gets a numbered chapter title per exercise (`plan_edit` takes `chapter` on a shot). No text ever covers the head or the torso: [style profile v1](style-profile-v1.md#8c-never-over-the-body-p6-06).

**Text rendering, and the seam for an overlay layer.** Texts, captions and the watermark are drawn by one stage: the `ass` filter that the compiler adds after the join (`[vj]` to `[vout]`, `Subtitles.filter()`), from a script built by `render/ass.go`. A motion-graphics overlay layer (rendered elsewhere, for word-by-word reveals, glow and animated cards) composites at the same point: an `overlay` of its frames onto `[vj]` before or instead of the `ass` stage, placed by the same `timeline.PlaceTextAvoiding` and `subject.FitText` so it obeys the same safe zone and the never-on-the-body rule. Nothing else in the video graph needs to know about it. (Built in P6-07, D-35: [motion-graphics-v1](motion-graphics-v1.md); the overlays go before the `ass` stage.)

**Angles.** A long set is not one 10 s tripod take: with two angles of an exercise the editor switches between them every 2 to 5 s at rep boundaries; with one angle it runs with the living camera and a punch-in.

**The checks added** (`check_edit`; no existing check was loosened except where a song or the vibe now says what is right):

| Check | Fails when |
|---|---|
| `off_beat_cut` | a cut is more than 60 ms from a beat or a strong onset of the song (a transition counts at its start or its middle); n/a without a song |
| `hook_misses_drop` | the first cut is more than 60 ms from the song's drop; the fix names the `source_offset_ms` that moves the drop onto it |
| `static_shot` | a shot over 2 s has no camera motion and the footage does not move by itself; skipped at calm for a creator whose own camera is static |
| `no_transitions_at_lively` | at lively or hype all cuts are hard, at hype fewer than a quarter are transitions, or the cut on the drop is hard |
| `transition_variety` | the same transition three times in a row |
| `text_without_entrance` | at lively or hype a text has no entrance or no exit |
| `chapter_titles` | the plan has two or more chapters and the edit fewer numbered titles |
| `text_over_subject` | (stricter) any text covers the torso, a hook or chapter title has to be moved to another time or left out |

**The motion-graphics checks** (P6-07, `editor.v2.8`; a graphics item is a title for `hook_title` and, as a `chapter_card` or a numbered title, for `chapter_titles`):

| Check | Fails when |
|---|---|
| `overlay_readable` | a graphic is fully on screen for less than 1.2 s before its exit starts (the template's own timing, `motion.Schedule`); the fix names the duration |
| `overlay_over_subject` | a graphic the renderer leaves out (no time clear of the body), a hook or chapter graphic it moves to another time, or one set not to avoid the subject that covers it (`subject.FitText`, as for text) |
| `overlay_legibility` | the letters' contrast with the picture behind their box, measured on the preview just before or after the graphic, is under 3:1 (2:1 with a shadow; a plate or an outline passes) |

`hook_length` lets a hook run until the song's drop (at most 5 s), `beat_sync` counts a transition at its middle, and `effect_density` allows 0.6 effects a cut at lively and 1.0 at hype (0.5 at calm, as before).

**Verified** without a model: a hand-written montage of the founder's footage with the synthetic 124 BPM track (`/tmp/cf-p606`, local only) renders with motion on every shot, the cuts and transition middles on the beats, the hook ending on the drop, the chapter titles in the top band and none on a torso, at -14.0 LUFS; the checks pass 17 of 17 that apply, where the old static three-take cut fails 9 (`static_shot`, `no_transitions_at_lively`, `text_without_entrance`, `chapter_titles` among them).

## The pace, the hook title and the song (P6-06 follow-up, `editor.v2.7`)

> Built after a live run of `editor.v2.6` at lively ($0.60, 17 of 19 checks): every cut on the beat and the hook on the drop, and still
> slow and off. Five shots in 20 s (a median of 3.97 s against the creator's measured median of about half that), a hook with no title
> (the renderer moved it to the next shot: no room above a high head), a title copied word for word from one of the creator's reference
> videos, an ending pose held for seconds, and gym clips whose own sound the analysis had read as music. **Nothing below is about one
> creator**: every number comes from the creator's style profile, scaled by the vibe and by the creator's own pace setting, or from
> general defaults written in one place.

**The pace targets** (`pace.go`). The profile measures every shot of the creator's videos that have a cut: the median, the 90th
percentile (`pacing.shot_p90_s`) and the last shot of each video (`pacing.ending_s`, how long they hold the ending). The targets of an
edit are those three numbers times the vibe's factor (calm 1.3, lively 1.0, hype 0.7) times the creator's pace setting (`slower` 1.25,
`like_my_videos` 1, `faster` 0.8; the input the "Your style" feature fills, `CheckInput.PaceSetting`):

| Target | From | Check |
|---|---|---|
| the median shot, within ±40 % | the creator's measured median shot | `shot_pacing` (with a profile; without one the type's maximum, as before) |
| the longest shot, the hook and the ending aside | the creator's measured 90th percentile shot | `long_shot` (new) |
| the longest held ending | the creator's measured ending; 1.6 times that when it moves (a push with a punch-in on a beat) | `held_ending` (new) |

The vibe factors are a beat either way on the shots this is about: a shot of 3 to 4 beats gets about one beat more at calm and one less
at hype, the difference the playbook draws between a held moment (2 to 4 beats) and a hype cut (1 to 2), and they stay inside a
creator's own spread (their quartiles run about 0.5 to 1.6 times their median). Lively is "like the creator's own cut-up videos". At
calm a creator whose videos are long takes keeps them (no targets; the long-take thresholds of the profile hold). A profile stored
before the two new measures is made again from its stored measures when it is read (`LoadProfile`); measures that cannot be read fall
back to ratios of the median (7/4 and 5/4, the ratios of the defaults). **Without a profile** the defaults are counted in beats of the
default 124 BPM grid, from the playbook: a typical shot of 4 beats (1.94 s), the longest of 7 (a full rep, 3.39 s), an ending of 5
(2.42 s); talking and tutorial videos follow the speech and have no targets. plan_edit states the targets and notes a shot list that
misses them; at lively and hype the style's pacing dimensions (cuts, long takes, shot length) give way to them, since a creator whose
videos are partly single takes would otherwise forbid the pace of their own cut-up videos.

**Slow lifts.** A rep of 4 to 5 s is longer than most creators' shots. The workout playbook (`workout.v4`) and the prompt name three
tools, for when the pace target needs them: cut at the rep's turning point (half a rep, on the beat), switch at the turning point to
another angle of the same exercise (front and behind), or play the rep faster. find_moments now gives each rep's `turn_ms`, the
analysis's measured turning point (stored as `peak_ms` since the first analysis; read by `ListAssetSpans` as `turn_ms`).

**The tolerance of `rep_boundaries`** was not loosened by taste. It is what can be measured: half a sample of the analysis (50 ms at
10 Hz), the largest move of `snap_cuts_to_beats` (80 ms) and half an output frame (17 ms at 30 fps) add up to the 150 ms that was
there. Two things were wrong with a fixed 150: the snap and the frame are output time, so at a speed over 1 they are longer in the
source (the tolerance is 50 + 100 x speed ms), and a slow rep lingers at its turns: within 5 % of a rep's length of a turning point
the movement has covered under 5 % of its range (1 - cos(2 pi x 0.05) = 0.049), so a 4.3 s rep accepts 217 ms (at most 250). The edge
the live run flagged (153 ms from the end of a 4.3 s rep, on a beat, at speed 1.018) passes on the second ground; the same 153 ms on a
1.5 s rep still fails. Being on a beat is not a reason by itself: the snap's 80 ms is already inside the 150.

**The hook title stays in the hook** ([style profile v1](style-profile-v1.md), section 8d): the placement now shrinks a title to the
smallest legible size, wraps it to two or three lines to stack it beside the head over the background, and only then moves it in time;
`text_over_subject` then fails with the room it found in pixels and a fix in numbers. **Titles are new**: the profile's title templates
are forms to fill ("<TOPIC> 101" with the length of the topic) and never the creator's titles, and `title_copied` (new) fails a title
that is, without regard to case or spaces, the title of one of the references.

**The song is the creator's.** The analyze job measures the sound of every asset with sound, and a gym plays music, so footage clips
had music analyses. The editor now treats only an audio file the creator gave, or a video sound the creator chose for the edit (a
`creator_audio` item already on the timeline), as a song: list_footage and the opening message show song lines for those only, and
get_music refuses a footage clip (`TestOnlyTheCreatorsAudioIsTheSong`).

**Verified** without a model: the live run's spec rendered with this branch (`cfctl render --profile preview --provider docker
--subjects`, local files only) keeps the title in the hook, stacked in three lines at scale 0.6 over the wall beside the head; with
boxes that do not measure the head (quicklook.v2) it is moved to 2.3 to 4.15 s exactly as in the live run. The checks over that spec
fail `shot_pacing` (3.97 s against a target of about 2.2 s for the test fixture's profile) and `title_copied`.

