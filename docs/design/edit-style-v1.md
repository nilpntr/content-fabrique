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
  - Generated SFX (eleven categories with variants, [P3-11](#sound-p3-11)) are rendered into exports. They are synthesised by us, so there is no licensing issue.
  - Source audio is ducked under an accent where it carries speech (the renderer does it), and muted where it does not.
  - The rules for density, sync and the arc are in [Sound (P3-11)](#sound-p3-11) below.
- **The ending** loops back to the hook (a held pose or the same frame), so the video replays smoothly.

## 4. What we build, in order

| # | Area | What | Task |
|---|---|---|---|
| 0 | Understand | Transcripts with word timings (P2-05, now urgent), speech structure (sentences, filler words, pauses), **face and subject detection for reframing** (MediaPipe, D-16), genre classification and the creative plan step | P2-05, new P2 task, editor v2 |
| 1 | See | Fix frame sampling; ffmpeg-only signals per clip (motion energy, exposure, sharpness, scene changes); shots; **reps, holds and dead time**; a `find_moments` tool; a per-clip analysis summary in the run context | `analysis-motion-reps` (P2-02/03/09 slices) |
| 2 | Render | **Motion presets** (punch-in, push-in, pull-back, shake), **speed ramps** (variable speed within one item), **transitions** (whip, zoom-through, flash, dip; existing crossfade), **auto exposure match plus grade presets**, **generated SFX** (an `sfx` audio kind: whoosh, impact, riser, synthesised in ffmpeg; redesigned in P3-11 as eleven categories with variants) | P3-10 (extended), P3-05. **Built (part A):** motion presets, speed ramps, `whip_left`, `whip_right`, `zoom_through`, `flash` (see section 6 of the [timeline note](timeline-v1.md)). `glitch` is not built (it has no cheap, deterministic form in ffmpeg). **Built (part B):** the `auto` and `gym_moody` looks, `look.match` with the bounded `exposure_ev` hint, and the generated `sfx` audio kind (whoosh, impact, riser, swish). **Built (P3-11):** sound design v2 (eleven categories with variants, loudness matching, ducking, a limiter with headroom) and the model-free "ears" (`internal/audiocheck`), see [audio v1](audio-v1.md). The ops the editor uses are below |
| 3 | Rhythm | A default tempo grid when no sound is chosen (beat markers written into the spec); the editor uses `snap_cuts_to_beats`; the preview beat guide | P3-09 is done; editor work |
| 4 | Editor v2 | The understand step and genre playbooks (prompt modules, versioned), the style guides above; a planning step (shot list with beats before ops); a **self-review loop** (after the preview, check the contact sheet plus frames at every cut against a checklist, then revise once); use of the new presets | P4-03 v2, P4-05 slice |
| 5 | Measure | Automatic edit checks: hook ≤ 2 s; shot-length spread; cuts on rep boundaries (±150 ms); no dead time; exposure spread across cuts; effect density; text in safe zones; duration in range; **sound** (loudness, true peak, first sound, silent gaps, the ending, sync to the cuts and the effects: `audiocheck.Check`, [P3-11](audio-v1.md), to be wired into `check_edit` by editor v2). Plus `cfctl eval editor` over a small local scenario set (the founder's clips) that compares versions | P4-07 slice |

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

## Sound (P3-11)

> The rules below come from the measured profile of seven professional launch films and the practice notes of **earshot** ([github.com/Leonxlnx/earshot](https://github.com/Leonxlnx/earshot), MIT licence, Copyright (c) 2026 Leon Lin; its `docs/launch-video-sound.md` and `docs/motion-sfx-techniques.md`). "Measured" means measured by earshot on those seven films (small sample: what pros did here, not laws); "practice" is established professional practice it cites. We took numbers and ideas, not code. The sound design and the ears that check it are in [audio v1](audio-v1.md).

- **Density: restraint.** More sounds is not better. The films have a dense groove (2.5 to 8.2 onsets a second, measured) with only a handful of moments that jump out (1.2 to 1.7 salient hits a second). Sound the moments that carry meaning, the reveal, the logo, the key UI action and the section change; small motion rides on the music. About **one effect per 3 to 4 cuts, at most 4 per 10 s** (our estimate), and no hover or micro-motion sounds. A cluttered effect layer on busy music turns into noise.
- **Sync: on the frame, not on every cut.** An effect lands on its frame: a **whoosh** peaks on the cut (or on the fastest frame of the move) and starts 0.2 to 0.6 s before it, so put its `at_ms` that long before the cut and match its length to the move (`fast` for a quick whip, `heavy` for a big one); a **click** is on the frame the cursor presses, not when the UI reacts; an **impact** or **sting** is exactly on the reveal frame. A sound a frame or two off breaks the illusion: let it lead by up to a frame, never lag by two. A hit on every cut is **not** what the pros do: sound on visual events is no better than chance in most of the references (81 % of cuts have an onset within 100 ms, but 86 % do by chance, measured), because dense music covers the cuts. Deliberate accents go on the moments that matter. With a sparse sound track (no dense music) every cut needs some sound.
- **The arc: open strong, hold the body, land the ending.** Sound from frame 0, no silent cold open (the first sound within 0.04 to 0.48 s, median 0.13 s, measured). A steady body: the loudest 3 s sit only 1.1 to 3.5 LU above the programme (measured), so do not make the viewer wait for a climax. Land the ending at body level with a short fade or a sting finishing within about 0.5 s of the last frame; avoid a long fade-out and dead air (measured: a median 0.04 s of silence after the sound). Shape the energy with arrangement (layers in and out, a stop-down before the reveal), not with big level changes.
- **The big reveal.** A riser, then a stop-down (0.1 to 0.5 s of near silence, practice; 4 of 7 films drop the sound 10 dB or more for 0.15 to 1.5 s, measured), then an **impact with a sub drop** on the reveal frame. Once or twice per film, not on every scene. Reserve the longest, widest whoosh for the biggest move.
- **Loudness.** Master at about **-14 LUFS integrated, true peak at most -1 dBTP** (practice; the renderer does this). Up to about -10 for a punchy social cut. The effects are mixed after the normalisation, at a level matched to their category, with a -1.5 dBFS ceiling.
- **Voice-over.** Duck the music 3 to 6 dB under speech and keep effects out of its way: never put a big hit on top of a key word (practice). The renderer ducks the programme a few dB under an accent that overlaps speech from a clip's own sound.
- **UI foley.** One click per on-screen click, short, bright (2 to 6 kHz) and well under the hits (about 10 to 15 dB under the music's peaks, practice); notifications short and soft.

The ears (`audiocheck.Check`, a model-free report on the rendered video) check these: first sound, time to body level, silent gaps, dead air, the ending level, the share of cuts and of planned effects with an onset on their frame (with the chance level next to it), effects per 10 s, the longest stretch with nothing new, and clipping. Each target in the report names its source, and the ones that are our estimates say so.
