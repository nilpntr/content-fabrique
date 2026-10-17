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
| 3 | Rhythm | A default tempo grid when no sound is chosen (beat markers written into the spec); the editor uses `snap_cuts_to_beats`; the preview beat guide | P3-09 is done; editor work |
| 4 | Editor v2 | The understand step and genre playbooks (prompt modules, versioned), the style guides above; a planning step (shot list with beats before ops); a **self-review loop** (after the preview, check the contact sheet plus frames at every cut against a checklist, then revise once); use of the new presets | P4-03 v2, P4-05 slice |
| 5 | Measure | Automatic edit checks: hook ≤ 2 s; shot-length spread; cuts on rep boundaries (±150 ms); no dead time; exposure spread across cuts; effect density; text in safe zones; duration in range. Plus `cfctl eval editor` over a small local scenario set (the founder's clips) that compares versions | P4-07 slice |

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
