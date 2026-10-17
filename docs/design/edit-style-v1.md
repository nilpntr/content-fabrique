# Design note: edit style v1, from "correct" to "viral" (fitness shorts)

> Status: **accepted** · 2026-10-17 · Written after the first live Opus 5.5 edit of real gym footage (Gino's back-day clips; $0.22 per run). The structure was right; the craft was rough. This note says what "good" means, which renderer features, analysis signals and editor behaviours get us there, and how we measure it. It extends [timeline-v1](timeline-v1.md), [agent-runtime-v1](agent-runtime-v1.md), [D-02](../05-decisions.md#d-02-edit-representation-declarative-timeline-json--deterministic-renderer), [D-03](../05-decisions.md#d-03-music-cue-only-by-default) and [D-26](../05-decisions.md#d-26-video-effects-are-presets).

## 1. What the first edit got wrong

| Symptom | Cause |
|---|---|
| Every cut was exactly 3.0 s | No sense of rhythm, no beat grid, no guidance on how long shots should be |
| Cuts landed mid-rep, and two "reps" were the lifter standing between sets | The editor saw 3–8 stills per 90 s clip, and had no rep or dead-time signal |
| Brightness jumped between angles | The look was the same preset everywhere; nothing evened out exposure |
| Plain hard cuts and small static labels | There were no motion presets, no transition styles, no SFX, and only basic title animation |
| The editor never really checked its work | No self-review step and no measurable quality bar |

## 2. The style we're aiming for (fitness shorts, 15–35 s)

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

## 3. What we build, in order

| # | Area | What | Task |
|---|---|---|---|
| 1 | See | Fix frame sampling; ffmpeg-only signals per clip (motion energy, exposure, sharpness, scene changes); shots; **reps, holds and dead time**; a `find_moments` tool; a per-clip analysis summary in the run context | `analysis-motion-reps` (P2-02/03/09 slices) |
| 2 | Render | **Motion presets** (punch-in, push-in, pull-back, shake), **speed ramps** (variable speed within one item), **transitions** (whip, zoom-through, flash, dip; existing crossfade), **auto exposure match plus grade presets**, **generated SFX** (an `sfx` audio kind: whoosh, impact, riser, synthesised in ffmpeg) | P3-10 (extended), P3-05 |
| 3 | Rhythm | A default tempo grid when no sound is chosen (beat markers written into the spec); the editor uses `snap_cuts_to_beats`; the preview beat guide | P3-09 is done; editor work |
| 4 | Editor v2 | The style guide above in the prompt; a planning step (shot list with beats before ops); a **self-review loop** (after the preview, check the contact sheet plus frames at every cut against a checklist, then revise once); use of the new presets | P4-03 v2, P4-05 slice |
| 5 | Measure | Automatic edit checks: hook ≤ 2 s; shot-length spread; cuts on rep boundaries (±150 ms); no dead time; exposure spread across cuts; effect density; text in safe zones; duration in range. Plus `cfctl eval editor` over a small local scenario set (the founder's clips) that compares versions | P4-07 slice |

Each step gets a live Opus run on the same footage, with the edit checks from step 5 shown next to the previous run's.

## 4. Spec additions (all additive to timeline v1)

- **Video items:**
  - `motion: {preset: punch_in|push_in|pull_back|shake|none, at_ms?, strength?}`
  - `speed_ramp: [{at_ms, speed}]`, which replaces a single `speed` when present; the output duration is computed by integrating the speed over the ramp.
  - `look.preset` gains `auto` and `gym_moody`; `look.match: true` applies the per-shot exposure hint from the analysis.
- **Transitions:** `transition_in.type` gains `whip_left|whip_right|zoom_through|flash|glitch` (glitch only if it is cheap and deterministic).
- **Audio items:** `kind: "sfx"` with `sfx: whoosh|impact|riser|swish` and `at_ms`. These are generated by the renderer, so there's no asset and the export is `licensed`.
- **Markers:** the editor may write a beat grid (`kind: beat|downbeat`) from a chosen tempo. `snap_cuts_to_beats` reads them.

Every addition comes with schema fixtures, ops, validation rules (for example, a speed ramp must not exceed the source length, and a motion preset needs a long enough shot), compiler support and golden renders in all four formats.
