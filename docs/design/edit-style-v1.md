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
  - Every shot is evened out to a common exposure and contrast, then a single grade is applied ("gym moody": deep blacks, warm skin, a slight vignette).
  - Nothing flickers between angles.
- **Text.**
  - One bold title for the hook, with kinetic animation (pop or slam, synced to the beat).
  - Exercise labels arrive with the exercise and are short, uppercase and high-contrast.
  - Optional word-highlight captions when there is speech.
  - Everything sits inside the safe zones.
- **Sound.**
  - The trend sound stays `cue_only` (D-03).
  - The preview plays the beat guide.
  - Generated SFX (whoosh, impact, riser) are rendered into exports. They are synthesised by us, so there is no licensing issue.
  - Source audio is ducked or muted unless there is speech.
- **The ending** loops back to the hook (a held pose or the same frame), so the video replays smoothly.

## 4. What we build, in order

| # | Area | What | Task |
|---|---|---|---|
| 0 | Understand | Transcripts with word timings (P2-05, now urgent), speech structure (sentences, filler words, pauses), **face and subject detection for reframing** (MediaPipe, D-16), genre classification and the creative plan step | P2-05, new P2 task, editor v2 |
| 1 | See | Fix frame sampling; ffmpeg-only signals per clip (motion energy, exposure, sharpness, scene changes); shots; **reps, holds and dead time**; a `find_moments` tool; a per-clip analysis summary in the run context | `analysis-motion-reps` (P2-02/03/09 slices) |
| 2 | Render | **Motion presets** (punch-in, push-in, pull-back, shake), **speed ramps** (variable speed within one item), **transitions** (whip, zoom-through, flash, dip; existing crossfade), **auto exposure match plus grade presets**, **generated SFX** (an `sfx` audio kind: whoosh, impact, riser, synthesised in ffmpeg) | P3-10 (extended), P3-05. **Built (part A):** motion presets, speed ramps, `whip_left`, `whip_right`, `zoom_through`, `flash` (see section 6 of the [timeline note](timeline-v1.md)). `glitch` is not built (it has no cheap, deterministic form in ffmpeg). **Built (part B):** the `auto` and `gym_moody` looks, `look.match` with the bounded `exposure_ev` hint, and the generated `sfx` audio kind (whoosh, impact, riser, swish). The ops the editor uses are below |
| 3 | Rhythm | A default tempo grid when no sound is chosen (beat markers written into the spec); the editor uses `snap_cuts_to_beats`; the preview beat guide | P3-09 is done. **Built (editor v2):** the prompt's pacing and rhythm section and the order of the ops (section 7) |
| 4 | Editor v2 | The understand step and genre playbooks (prompt modules, versioned), the style guides above; a planning step (shot list with beats before ops); a **self-review loop** (after the preview, check the contact sheet plus frames at every cut against a checklist, then revise once); use of the new presets | P4-03 v2, P4-05 slice. **Built (editor v2, prompt `editor.v2.0`):** `plan_edit`, six playbooks, `check_edit`, the plan and check gates, the use of every preset in section 5 with restraint. **Not built:** a transcript read path for the talking playbook (the transcripts exist, the editor has no tool for them), face and subject detection, and a look at the frames at every cut (the objective checks replace the checklist for now) |
| 5 | Measure | Automatic edit checks: hook ≤ 2 s; shot-length spread; cuts on rep boundaries (±150 ms); no dead time; exposure spread across cuts; effect density; text in safe zones; duration in range. Plus `cfctl eval editor` over a small local scenario set (the founder's clips) that compares versions | P4-07 slice. **Built:** the checks (section 7) and `cfctl eval editor --workspace <wks> --project <prj> [--version N]`, which prints them for any stored version with no model calls. **Not built:** the scenario set and the comparison between versions or runs (run the command per version for now), and per-genre checks beyond the thresholds of section 7 (filler words, captions in sync, the reveal within 3 s) |

The checks are per genre (for example, talking: filler words removed, captions in sync, speaker framed; unboxing: reveal within the first 3 s). Each step gets a live Opus run on the same footage, with the edit checks from step 5 shown next to the previous run's.

## 5. Spec additions (all additive to timeline v1)

- **Video items:**
  - `motion: {preset: punch_in|push_in|pull_back|shake|none, at_ms?, strength?}`
  - `speed_ramp: [{at_ms, speed}]`, which replaces a single `speed` when present; the output duration is computed by integrating the speed over the ramp.
  - `look.preset` gains `auto` and `gym_moody`; `look.match: true` applies the per-shot exposure hint from the analysis.
- **Transitions:** `transition_in.type` gains `whip_left|whip_right|zoom_through|flash` (built); `glitch` was optional and is left out. `whip` stays as an alias of `whip_left`.
- **Audio items:** `kind: "sfx"` with `sfx: whoosh|impact|riser|swish` and `at_ms`. These are generated by the renderer, so there's no asset and the export is `licensed`.
- **Markers:** the editor may write a beat grid (`kind: beat|downbeat`) from a chosen tempo. `snap_cuts_to_beats` reads them.

### Where the editor learns the new presets

The new craft features reach the editor agent as edit ops, whose descriptions in `ops.v1.schema.json` are its tool documentation (the editor prompt module that teaches *when* to use them is part of editor v2, step 4 above):

- `set_motion` (`punch_in` on a beat with `at_ms` relative to the clip, `push_in` or `pull_back` over a whole clip, `shake` on an impact), `set_speed_ramp` (fast into slow at the peak contraction), `set_transition` with `whip_left`, `whip_right`, `zoom_through` and `flash`: about one effect per 3 to 4 cuts.
- `set_look` with `gym_moody` for the single grade of a fitness edit, and with `auto` or `match: true` to even out exposure between angles. `exposure_ev` is written by the analysis, not guessed: a bounded nudge of -1 to 1 stops that must be computed from highlight or midtone percentiles, never from mean brightness (low-key gym footage is dark and correct).
- `add_sfx` (`whoosh` on a whip or a cut, `impact` on a punch-in or the drop, `riser` started 1.5 s before the moment it leads to, `swish` for small moves), moved with `move_item` and removed with `remove_sound`. The trend sound stays `cue_only`.

Every addition comes with schema fixtures, ops, validation rules (for example, a speed ramp must not exceed the source length, and a motion preset needs a long enough shot), compiler support and golden renders in all four formats.

## 6. Editor v2 as built (`internal/agents/editor`, prompt `editor.v2.0`)

**The flow of a run.** Understand (`find_moments`, `view_clip`) → `plan_edit` → `edit_timeline` (in the order of section 7) → `render_preview` → `check_edit` → at most one revision (`edit_timeline`, `render_preview`, `check_edit`) → `finish`. The tools are in [agent-runtime-v1](agent-runtime-v1.md) section 5; the gates are the first `edit_timeline` of a run (refused without a plan) and `finish` (refused without a preview and a `check_edit` of the newest version the run made). Both gates keep their record in the worker and fall back to the run's events, so a run that resumes in another worker is not asked to plan or check again. The revision limit is guidance in the answer of `check_edit` ("this was the check after your revision pass: do not revise again"), not a refusal, so a run that cannot fix a check can still finish and say why; the run's budgets still bound it.

**The plan** (`plan_edit`, `plan.go`). The tool is strict. The content type and the playbook come from an enum (talking, unboxing_product, workout, vlog, tutorial, general), and the tool checks the bounds the strict schema cannot hold: story and hook at most 200 characters, arc at most 400, ending at most 200, a shot's `what` at most 160, `target_length_s` 5 to 120, `tempo_bpm` 0 (no beat grid) or 60 to 200, 1 to 30 shots of 1 to 64 beats each, an `asset_id` that is one of the project's clips, and no effect the build cannot make. It answers with the playbook of the chosen genre, the length of a beat and the shot list in milliseconds, and with notes when the shot list does not match the target length, has more than two equal shots in a row, or opens with a hook over 2 s. The plan is recorded as an artifact event, for the run feed to show:

```json
{"kind": "creative_plan", "tool_use_id": "toolu_...", "data": {
  "version": 1,
  "content_type": "workout", "playbook": "workout", "playbook_version": "workout.v1",
  "story": "...", "hook": "...", "arc": "...", "ending": "...",
  "target_length_s": 25, "tempo_bpm": 124, "pace": "building",
  "beat_ms": 484, "estimated_length_ms": 24200,
  "shots": [{"role": "hook", "what": "...", "asset_id": "ast_...", "beats": 3, "effect": "punch_in", "length_ms": 1452}],
  "prompt_version": "editor.v2.0"}}
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
              "cuts_on_beat": 5, "cuts": 5, "brightness_max_jump": 0.07, "brightness_range": 0.12},
  "passed": 10, "failed": 1, "skipped": 1, "nth": 1}}
```

| Check | Passes when | Notes |
|---|---|---|
| `hook_length` | the first shot is at most 2 s | |
| `hook_title` | a text starts by 0.5 s, or captions are enabled | |
| `shot_pacing` | the median shot is at most 3 s (workout), 3.5 s (unboxing, general), 4 s (vlog), 5 s (tutorial) or 6 s (talking) | the type comes from the plan; without a plan it is general |
| `shot_variety` | never more than two shots in a row within 8 % (or 100 ms) of each other, and not all within a 12 % spread (5 shots or more) | |
| `pace_builds` | the second half's median shot is at most 15 % over the first half's, the last shot left out | workout, unboxing, vlog, general, 6 shots or more |
| `rep_boundaries` | at least 85 % of the shot edges on clips with reps are within 150 ms of a rep start, a rep end or the turning point half-way between, or inside a hold | the half-way point is what makes "the strongest half of a rep" a legal cut |
| `dead_time` | at most 5 % of the screen time is dead time | counted in output time, from the dead moments |
| `effect_density` | between 0.15 and 0.5 effects per cut (the lower bound for workout, unboxing, vlog and general with 4 cuts or more), and at most 4 speed ramps | an effect is a motion preset, a transition that is not a cut, or a speed ramp |
| `text_safe_zone` | the version has no `unsafe_text` or `text_overlaps_captions` issue | the validator already refuses `unsafe_text` for a stored version, so this mostly reports |
| `duration` | 15 to 45 s, or within a quarter of the plan's target | |
| `brightness_jump` | no cut jumps more than 0.25 in the p90 luma of the lit part of the picture | the exposure of each shot is the median, over its samples, of the 90th percentile luma of the frame (`analysis.Series.ExposureBetween`): not the mean, so low-key footage is not penalised |
| `beat_sync` | at least 70 % of the cuts are within 2 frames of a beat marker | a workout, unboxing, vlog or general video with no markers fails; talking and tutorial skip it |

`beat_sync` was not in the list of step 5; it is the check for step 3 (the editor writes the grid, so it should be measured).

**The order of the ops.** `snap_cuts_to_beats` cannot move a cut next to a clip with a speed ramp (it leaves it and warns `cut_not_snapped`), and a speed change, a transition or a ramp changes a shot's length and so moves every later cut. So the prompt says: (1) the clips, trims and reframes, and in the same batch everything that changes a length (`set_speed`, `set_transition`, `set_speed_ramp`, tuning a ramped shot to whole beats with `trim_item`); (2) `snap_cuts_to_beats` once, with `write_markers` and a small `max_shift_ms` (80), because a snap moves the edges of both its shots and would otherwise pull them off their rep boundaries (a second snap moves them again: use 60 or less); (3) what does not change timing: `set_motion`, texts, `add_sfx`, captions and clip audio, and last the look. To make rep-based shots whole beats without moving their edges, set the shot's speed to `(out_ms - in_ms) / (beats x beat_ms)` (a 3000 ms rep as 6 beats at 124 BPM is speed 1.03).

**Exposure in the analysis line.** The stored summary's `exposure_gain` and key brightness are mean-based and called the founder's clips dark, with a gain of x2.5 to match. The editor's analysis line is now computed at read time from the stored `luma` and `high` series (`ListAssetExposureSeries`), with no change to the stored format and no version bump: "low-key (p90 luma 0.18)" for a dark picture with a lit part, "underexposed (p90 luma 0.02)" only when even the lit part is near black, "bright" at a p90 over 0.92 with a mean over 0.65, nothing otherwise, and never a gain. The thresholds come from the founder's clips, which measure a p90 of 0.10 to 0.33 and a mean of 0.00 to 0.07 and render correctly, so underexposed starts at a p90 of 0.06. The shot and moment descriptions stored by the analysis ("dark", "well lit") and the exposure score of a rep or hold still come from the mean-based key brightness; fixing them needs a new analysis version and a re-run (`cfctl assets analyze --all`), and the prompt tells the editor to go by the clip's line.

**Measuring.** `cfctl eval editor --workspace <wks_...> --project <prj_...> [--version N] [--json]` runs the same checks over a stored version (the current one by default), with the plan of the editor run that made it when the run wrote one. It costs nothing and writes nothing. (`--workspace` is required, as for the other `cfctl` commands: every store query is scoped to a workspace.)
