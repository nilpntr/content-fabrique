# Design note: audio v1, sound design and ears for the edit

> Status: **built** (P3-11; sound-ready exports and the ears in the editor, section 6, P3-12) · 2026-10-17 · Extends [timeline v1](timeline-v1.md) (the `sfx` item) and [edit style v1](edit-style-v1.md) (the sound section). It builds on [D-03](../05-decisions.md#d-03-music-cue-only-by-default) (trend sounds stay cue-only), [D-16](../05-decisions.md#d-16-licensing-policy-for-ml-and-media-components) (permissive licences only) and [D-26](../05-decisions.md#d-26-video-effects-are-presets) (effects are curated presets), and is recorded as [D-32](../05-decisions.md#d-32-audio-is-synthesised-and-measured-with-ffmpeg-no-audio-models).

The editor cannot hear. Until now it also had poor sounds to place: four single-layer noise effects that sounded cheap. This note covers two things, built together because the same measurements decide whether either is good:

- **Part A, sound design v2.** Eleven categories of generated sound effects, three or four variants each, built the way professionals build them (layers, bands, saturation, glue, a short dark room, a loudness match), deterministic, with no audio assets. Plus the mixing fixes: a limiter with its own latency compensation, a peak ceiling with true-peak headroom, loudness matching per category and ducking of speech under the hits.
- **Part B, ears for the editor.** `internal/audiocheck`: a model-free listening report on a rendered video, built from ffmpeg measurements: loudness, silence, onsets, the hook, sync to the cuts and to the planned effects, density and defects, with a pass/fail checklist and a PNG sheet of the sound on the contact sheet's time axis.

## 1. Where the ideas come from, and why no models

The ideas, the starting values and the measured launch-film profile come from **earshot** ([github.com/Leonxlnx/earshot](https://github.com/Leonxlnx/earshot), MIT licence, Copyright (c) 2026 Leon Lin): a tool that lets coding agents hear, judge and synthesise sounds. We read three of its documents as reference text: `docs/motion-sfx-techniques.md` (the design chain per sound), `docs/launch-video-sound.md` (loudness, the arc, sync, density, measured on seven launch films) and `docs/LIMITS.md`. We took **numbers and ideas only**. No code is used or ported: earshot is Python, and what we build is Go plus ffmpeg. Attribution is in the code comments that use its values (`internal/render/sfx_dsp.go`, `internal/audiocheck/analyse.go`), in this note and in [D-32](../05-decisions.md#d-32-audio-is-synthesised-and-measured-with-ffmpeg-no-audio-models).

We did **not** take its runtime:

- **No Python, no torch, no CLAP or AST models.** The models are about 1.4 GB, every CLI call loads them for 20 to 40 s (earshot's own `LIMITS.md`), the model weights would need the licence review of D-16, and the project is two days old (created 2026-10-15).
- **The measurements are the trustworthy part.** earshot's own limits page says so: loudness, timing, levels, spectrum, clipping and silence "are meters, not opinions"; what its models say about a mix is "right about half the time for exposed events, a quarter for events under the music" (a hint, not a verdict); and it "cannot judge taste and emotion". Optimising a model's score is the textbook way to be wrong in a confident voice (Goodhart): a mix that satisfies a classifier is not a mix that sounds good. A meter cannot be argued with, and a meter costs nothing.

So everything here stays Go plus ffmpeg in the media-tools sandbox, deterministic, with no audio assets and no model calls (cost: none).

## 2. Part A: sound design v2

### The design chain

Every effect is a handful of **mono layers**, each with one job and its own band, then processed as one sound. Starting values are earshot's (motion-sfx-techniques section 0, "the universal design chain"), tuned by measuring the result.

| Step | What | Where in the code |
|---|---|---|
| Layers | **Transient** (a noise burst above 1-2 kHz, its tail shortened by raising the envelope to a power: the crack the ear locks onto and the timing), **body** (80-800 Hz: a pitched sine that falls, a noise band, or a cloud of inharmonic partials for metal), **sub** (below 120 Hz: a clean sine with an exponential pitch drop, 3 ms ramp so it starts at a zero crossing), **air / tail** (a band of noise that moves; the room) | `sfx_recipes.go` |
| Band | each layer is high- and low-passed to its own range, so that two layers never fight in one band (earshot: two body layers sharing 200 Hz turn to mud) | per layer |
| Saturate | light tanh (2x oversampled so the new harmonics do not fold back) on **body and transient only, never the sub**: it survives small speakers | `saturate` |
| Shape | sample-accurate envelopes (an `aevalsrc` envelope through `amultiply`), not per-frame `volume` steps; transients are shortened, not smeared | `shape` |
| Room | a short dark room on the **body and the tail only, the sub and the transient stay dry**: a seeded noise impulse response (pre-delay 10-20 ms, exponential decay, low-passed at 3-6 kHz) through `afir` | `room` |
| Width | a Haas delay (7-17 ms) on the air and tail layers; transient and sub stay centred; the mono fold-down keeps its level (a test measures it) | `wide` |
| Glue | `acompressor`, 2:1 to 3:1, 15-25 ms attack (lets the transient through), 100-150 ms release, a few dB of gain reduction, then a high-pass at 22 Hz | `glue`, `buildSFXJitter` |
| Level | a per-variant correction that matches the category's level, then the item's jitter | `sfx_levels.go` |

**Curves.** A riser's change lands in the last third: its filter centre follows `lo (hi/lo)^(x^p)` (an exponential in log-frequency, p 2 to 3), its pitch rises 24 semitones along the same curve (the exact phase integral, written as a series), and its **level** rises along `exp(-K (1 - x^p))` (from -20 dB to 0 dB: linear amplitude would flatten out in dB, and a measurement caught that). An accelerating tremolo ends on a crest so the peak is at the end. A downlifter falls along the decelerating `1 - e^(-3x)`: fast first, slowing at the end, with a wetter room as the dry part dies. A pass-by (whoosh, swish) is `sin(pi x^k)^2` with its peak at 50-60 % of its length, and its band sweeps up and back (Doppler); it starts at -30 dB below its peak and not at silence, so its onset is where `at_ms` says.

**Determinism.** There are no assets and no `random()`: noise is a seeded `anoisesrc`, a glitch's "random" pitches come from a hash made of `sin()` steps, a shimmer's pings from a small generator (written out, not `math/rand`) seeded by the item id. The same spec renders the same samples on every machine (to the float precision of ffmpeg's expression evaluator), and the tests hash the PCM.

### Categories and variants

Every category has three or four named variants. The first is the default: an item with no `variant` (every spec written before P3-11) gets it, so nothing changes meaning. `auto` picks one from a hash of the item id.

| Category | Variants (default first) and natural length | What it is for | Layers |
|---|---|---|---|
| `whoosh` | `air` 0.6 s, `fast` 0.35 s, `heavy` 1.0 s | a whip, a cut, a move | two noise bands 1:1.6 that sweep together, a short room, width; `heavy` adds a swelling sine under it |
| `swish` | `thin` 0.3 s, `blade` 0.2 s, `cloth` 0.4 s | small moves | a quick bright air pass; `thin` and `blade` add a narrow whistle (Strouhal) |
| `impact` | `punch` 0.8 s, `deep` 1.4 s, `crack` 0.5 s, `metal` 1.2 s | a punch-in, the drop | transient + saturated body + clean sub with a pitch drop + room; `metal` has inharmonic partials |
| `riser` | `tension` 1.5 s, `sweep` 1.0 s, `pitch` 2.0 s, `long` 3.0 s | a build into a moment | noise and/or detuned tonal voices on exponential curves, tremolo, a growing room; the peak is the end |
| `downlifter` | `fall` 1.2 s, `short` 0.6 s, `tape` 0.8 s | the release after a hit, a new section | the mirror of a riser; `tape` is a tape stop |
| `sub_drop` | `boom` 1.0 s, `quick` 0.5 s, `long` 2.0 s | under an impact | a clean sine gliding down to 32-45 Hz, a saturated octave above for phones, a tick for the onset |
| `pop` | `bubble` 0.3 s, `tick` 0.15 s, `double` 0.35 s | an element appearing | a sine gliding up (Minnaert-like), 0.7 ms attack, a noise tick; `double` adds a daughter bubble |
| `click` | `ui` 80 ms, `soft` 100 ms, `tonal` 120 ms, `mech` 180 ms | an on-screen press, on the frame it happens | Farnell's band-passed noise click with a squared envelope; `mech` is the push-button composite |
| `glitch` | `stutter` 0.3 s, `crush` 0.25 s, `chirp` 0.4 s | a digital cut | gated tone slices of one frame (33 ms) with stepped pitch, bit-crushed noise, data chirps; deterministic |
| `shimmer` | `sparkle` 1.2 s, `wash` 1.5 s, `rise` 1.0 s | something magical or new | a cloud of short pings on a pentatonic scale at 2-8 kHz, a high wash, an airy room |
| `sting` | `hit` 2.5 s, `bright` 2.0 s, `dark` 3.0 s | a logo or a reveal | an impact with a long sub, then a tonal bloom (root, fifth, octave; no third, so it does not fight the music's key) through a low-pass that opens |

### Variants and the round-robin

The schema gains an optional `variant` on an `sfx` item and on `add_sfx` (the JSON Schema, the Go types, the ops, validation, the diff summary and the TypeScript types). The variant must be one of the category's own (the schema enforces it per category, so a hostile or misplaced value fails validation with one error, and the compiler and the op check it again).

Repeated sounds must not repeat: every item gets a **deterministic jitter from a hash of its id** (a SHA-256 prefix: `timeline.SFXHash`): pitch within half a semitone, filter centres within 12 %, length within 6 % (not for a riser, whose peak is its end), level within 1 dB and a different noise seed. Two items of one variant are never the same sample, and a re-render of one item is always the same. The variant `auto` additionally picks the variant from the hash, so six whooshes on one timeline are six different ones. (Earshot's round-robin ranges: pitch 0.3-1 semitone, level 1-2 dB, timing and decay 10 %.)

### Mixing

The effects are mixed in after the programme's loudness normalisation, at a fixed level, followed by a limiter, as before. (That is the mix of an edit with speech or music. An edit whose sound is a bed and effects is not normalised at all: section 6.) P3-11 changes four things, two of them the follow-ups of [issue #132](https://github.com/nilpntr/content-fabrique/issues/132):

1. **The limiter compensates its own delay** (`alimiter ... latency=1`) instead of the hand-measured 239 samples. `TestLimiterKeepsTimeAndLength` is the guard: an impulse comes out on the sample it went in on and the stream keeps its length, on ffmpeg 7.1 and 8.1.
2. **Headroom for true peak.** The ceiling is **-1.5 dBFS sample peak** (`sfxLimitDB`), not -1: the 0.5 dB is room for the inter-sample peaks that the AAC encoder and a player's resampler add. The old comment called -1 dBFS a true-peak limit; it is a sample-peak limit. `TestSoundEffectsLeaveTheProgrammeAlone` measures the true peak of the encoded mix with ebur128 and requires at most -1 dBTP. Measured per variant with ebur128: at the default gain every effect stays under -1 dBTP; at the loudest gain the spec allows (+6 dB), a bright effect driven into the limiter overshoots by up to 0.8 dB between the samples (riser `sweep` -0.8 dBTP, swish `blade` -0.7), so a +6 dB gain on a bright effect over a programme that is already at -1 dBTP can read just under -0.5. That is why gain_db is an accent knob, not a loudness knob.
3. **Loudness is matched per category.** Each category has a target (the loudest 50 ms RMS before the base level): impact and sting -11 dBFS, sub drop -10, riser -11, whoosh and downlifter -12, glitch -13, swish -14, shimmer -15, pop -17, click -21. Impacts are the loudest accents, whooshes sit under the picture, clicks and pops are detail 10 dB under a hit (earshot: UI foley 10-15 dB under the music's peaks). Within a category every variant is the same level, so a riser does not blast after a click and a deep impact is not quieter than a punch. The per-variant corrections are a table of measurements (`sfx_levels.go`) that `TestSFXLoudnessMatching -sfx-calibrate` regenerates; the test fails when a variant drifts more than 1 dB, on ffmpeg 7.1 and 8.1.
4. **Ducking.** Where an accent (impact, sting, sub drop, riser, downlifter, whoosh, glitch; not the detail categories) overlaps **speech from a clip's own sound** (the transcript words of the clips whose source audio is in the mix, merged across pauses under 250 ms), the programme is compressed by a sidechain compressor keyed on the effect: a few dB (3 to 5 at the default level, at most 6 at +6 dB of gain), 8 ms attack, 200 ms release. The reduction follows the effect's own level (a riser ducks most at its peak), it is deterministic, and with no overlap the programme is not touched at all (the graph has no sidechain). Earshot: duck 3-6 dB under voice. The test renders the same timeline with and without the transcript, so the difference is the ducking alone.

**Size.** An effect is 0.7 to 6 kB of the filter graph (a swept filter is a list of `asendcmd` commands, a shimmer a sum of pings). The graph is one argument of the ffmpeg process and Linux limits a single argument to 128 KiB, so `Compile` refuses a graph over `MaxFilterGraphBytes` (120,000 bytes: about 40 effects on top of a normal edit) with a message, instead of the sandbox failing with "argument list too long". The edit style asks for at most 4 effects per 10 s, so a real edit is far below it. The editor hears about it earlier than a failed render: validation has a **budget** (`timeline.SFXBudgetBytes`, 100,000 bytes, from a per-category upper bound `SFXGraphBytes` that `TestSFXGraphBytesAreAnUpperBound` keeps honest, about 30 effects) and refuses more with `too_many_effects` and a message that says to remove some; `add_sfx`'s description says so too. If a graph is still too long, the render fails with the reason `invalid_timeline: filter graph too long` (the failure reason is at most 64 characters and the compile message is not stored) and the editor's `render_preview` answer tells it to remove sound effects.

### Tests

- **Every category and variant renders deterministically** (the PCM is hashed twice and compared; another id gives another sample).
- **Measured shape**, per category: onset within 5 ms of its start (the first sample within 50 dB of the peak; the end-to-end test requires a frame at 30 fps), the energy of each layer's band (sub, body, transient), the tail length range, a riser that accelerates and peaks at its end, a downlifter that falls and decelerates, a whoosh that peaks in the middle and sweeps, a click that reaches its peak in under 3 ms, a sub drop whose pitch falls to 25-60 Hz.
- **In the mix**: no clipping and the peak under the ceiling at the loudest gain the spec allows, no DC offset, a clean start and a faded end, the mono fold-down within 5 dB of the stereo level.
- **D-03 and cue_only unchanged**: a trend sound next to an effect is still only a cue and never an input.
- **Hostile values**: every enum is checked by the schema, by the op and again by the compiler; the compiled graph holds only the allowed alphabet, with every category, variant and `auto`, and an item id with hostile text reaches the graph only as a hash.

### The gallery

`cfctl sfx gallery -o /tmp/cf-sfx/index.html` (development only) renders every category and variant, and three round-robin items per category, with the same graph the renders use, and writes a small static page: a player, the waveform (ffmpeg `showwavespic`) and the spectrogram (`showspectrumpic`) of each. The page and its files go where the flag says (a directory `<page>_files` next to it) and the command refuses a path inside the repository. It is how a person listens; the numbers above cannot say whether it sounds good.

## 3. Part B: ears for the editor

`audiocheck.Check(ctx, runner, mp4Path, timeMap, opts...) (Report, error)` runs **one ffmpeg job in the sandbox** over the rendered MP4 and analyses what comes back in Go. The graph splits the audio four ways: `ebur128` (integrated loudness, true peak, range and the momentary curve, its metadata written to a file), `silencedetect`, `astats` (per channel) and a mono 16 kHz dump that the Go side reads (onsets, the waveform). The file is untrusted by the usual rules: it is only a read-only input of the sandbox, what the job writes is read as regular files under a size cap, and the check refuses a file longer than 15 minutes.

### What it measures

| Metric | How | Notes |
|---|---|---|
| Loudness | ebur128: integrated LUFS, true peak (dBTP), loudness range (LU), momentary curve every 100 ms | the end level is the last 3 s against the integrated loudness |
| Silence | `silencedetect` at -50 dBFS for 100 ms | the longest gap and the silence at the very end |
| Onsets | the energy in 10 ms windows every 5 ms, in dB; an onset is a rise of 6 dB or more over the quietest of the previous 30 ms, above -55 dBFS, placed where the rise began; 12 dB or more is a "salient" hit (earshot counts those apart) | measured against synthetic bursts: within 12 ms |
| Hook | first sound: the first window above -50 dBFS; **time to body level**: the first moment the momentary loudness is within 3 LU of the integrated loudness | the momentary window is 400 ms, so even an instant start reads about 0.3 s |
| Sync | for each cut of the time map (the main-track entries after the first), the nearest onset: the share with an onset within **a frame** and within **100 ms**; the same for the planned sound effects (`WithSFX`), against their `at_ms`; and the **chance level**: the same cuts moved by fixed offsets of 300-900 ms | with dense sound almost every cut has an onset near it (earshot measured a median 81 % but 86 % by chance), so read the lift over chance |
| Density | sound effects per 10 s; onsets per second; the longest stretch with no onset | |
| Problems | clipping (at or over full scale with several samples at the peak, or over 0 dBFS), DC offset (over 0.5 % of full scale), channel imbalance (over 3 dB) | |

### The checklist, and where each target comes from

Every check has a value, a target, a source and, where the target is ours, an `estimate` flag. **earshot** means its measured profile of seven professional launch films (modern SaaS and AI product launches and motion reels, web-delivered MP4s with 128 kbps AAC audio; seven films are a small sample: "what pros did here, not laws"). **practice** is web delivery practice that earshot cites and did not measure.

| Check | Target | Source |
|---|---|---|
| Integrated loudness | -16 to -9 LUFS (about -14) | earshot: -22.2 to -7.3, median -14.8, web window -16..-9; practice: YouTube normalises to about -14 and only turns loud uploads down, Apple about -16, X, LinkedIn and autoplay players do not normalise |
| True peak | at most -1 dBTP (a decoded file: at most 0) | practice (AAC/MP3 encoding adds up to about 1 dB); earshot: 6 of 7 films measured above 0 dBTP, "a common flaw, do not copy it" |
| Loudness range | at most 12.5 LU | earshot: 1.7 to 12.2 LU, median 5.5 |
| First sound | within 0.5 s | earshot: 0.04 to 0.48 s, median 0.13 s; no silent cold open |
| Time to body level | within 1.5 s | **estimate** (earshot: 6 of 7 films open within -1.8..+2.2 LU of their level; the edit style wants the hook in 2 s) |
| Longest silent gap | at most 0.5 s (a deliberate stop-down up to 1.7 s warns) | earshot: no silent hole over 0.04 s except one film's deliberate 1.66 s; stop-downs are 0.15 to 1.5 s |
| Dead air at the end | at most 0.5 s | earshot: median 0.04 s; practice: finish within about 0.5 s of the last frame |
| The last 3 s reach body level | -4 to +6 LU against the programme | earshot: -2.2 to +5.6, median +0.2 |
| Cuts with an onset within 100 ms | at least 50 % | **estimate** (read the lift over chance) |
| Sound effects within a frame of their `at_ms` | at least 80 % | **estimate**: ours start on their first sample, so a miss means masked by the programme |
| Sound effects per 10 s | at most 4 (up to 8 warns) | **estimate**: the edit style asks for about one effect per 3-4 cuts; earshot: a cluttered effect layer on busy music turns into noise |
| Longest stretch with no new sound | at most 1.5 s (up to 3 s warns) | **estimate**: earshot's films have 2.5-8.2 onsets a second; a speech edit is sparser |
| No clipping, no DC offset, balanced channels | see above | practice / **estimate** |

A check that does not apply (no cuts, no effects) is `info`, never a failure.

### The output

`Report` has the plain-language **Summary** first (the verdict, the key numbers in a few short lines, then a `FIX:` or `LOOK:` line per failing or warning check with its fix), the structured sections, the **Checklist** and the **Sheet**, a PNG that is not part of the JSON.

The **sheet** draws the sound the way the contact sheet draws the picture: the x axis is the output time in ms, the same axis. Top to bottom: the waveform (red where it is at full scale), the momentary loudness with the integrated loudness and a band of 3 LU around it, an event lane (cuts white, planned effects magenta, onsets yellow, salient ones taller), silences shaded red across all of it, and the axis in seconds with cyan markers and numbers at the contact sheet's tile times (the frame 100 ms after each cut, `renderjobs.SheetOffsetMs`; the number is the tile's index), so a tile and a stretch of sound can be put side by side. It is drawn with `image/png` and a small built-in font: no dependency.

### How `check_edit` calls it (P3-12)

`check_edit` (`internal/agents/editor/audio.go`) listens to the **rendered preview of the version it checks**, with the same pattern as the brightness checks (`RenderMeasurer`: download the render from object storage, run ffmpeg in the media sandbox). It is wired through an optional interface, `AudioSampler`, that `*RenderMeasurer` also implements, so `CheckVersion` and `cfctl eval editor` get it without a new argument:

```go
res, err := sampler.Audio(ctx, ws, project, version, spec) // AudioResult{Report, Elapsed, Source}
```

- **The beat guide is excluded with a stem, not by subtraction.** A preview mixes a generated click on every beat for the editor's own review; the export has none. Subtracting it afterwards would need the guide's exact samples, the loudness normalisation's gain and the limiter's action, and a click under a hit would still bleed into the onset detector. So a preview with a guide writes the mix **without** the guide as a second output of the same ffmpeg run (`audio_stem.m4a`, AAC in an MP4 container, `[astem]` in the graph: the same core the export has, after the normalisation and the ceiling limiter), uploaded next to the video under a key derived from the render's (`storage.RenderAudioStemName`; no database column, no migration). `check_edit` measures the stem when the timeline has beat, downbeat or drop markers and the video itself otherwise. A preview made before the stem existed has none: the audio checks say so ("render_preview again") and do not guess. The stem is optional for the render: a failed upload is logged and the render is finished without it, and a caller that does not ask for it (`cfctl render` without `--stem`) has the pad read by a discarding output (raw PCM into the null muxer), so no AAC is encoded for nothing; `cfctl render --stem out.m4a` writes it.
- **Profile, automatic.** `audiocheck.WithSpec(spec)` plans the effects, takes the frame rate and chooses the profile (`ProfileFromSpec`): *sound-ready* when no clip is in `full` mode and the timeline has no licensed track or creator audio, *programme* otherwise (a trend sound is a cue and never counts; a `full` clip with a speed ramp is muted by the renderer and does not count either). It cannot know that an asset has no sound track, which the renderer does: such a clip is a bed clip that gives no bed, and `bed_gaps` says so.
- **Three checks in the report,** each with the ears' own one-line fixes: `audio_level` (has audio, loudness, true peak, clipping, DC, balance), `audio_bed` (bed present, no gap, steady; sound-ready profile only) and `audio_effects` (effects on their frame, over the bed, audible under a trend sound, density). A `fail` is a fail; a `warn` is shown as "borderline, not worth a revision" and passes, because the editor revises once on a failing check and an estimate close to its bar is not a reason to revise. Without a ready preview, or when the sampler errors, the three are `skip` with the reason ("there is no preview render of this version to listen to", "the preview's sound could not be measured") and the picture checks are unaffected.
- **Cost.** A preview without a beat guide is measured from the video itself, which the brightness pass has just downloaded as well (two downloads, two ffmpeg runs, no cache between them: the cheap fix is a shared local copy, not built). One download (the stem is 0.37 MB for 26 s at 128 kbps, a third of the 1 MB preview) and one ffmpeg pass: the step is `Metrics.audio_ms` in the check record and in the log. Measured on a 26 s workout edit: 0.28 to 0.34 s for the whole `RenderMeasurer.Audio` step (download, time map, ffmpeg, analysis) with the local provider, and 0.33 to 0.5 s for the ffmpeg run through the docker provider, about 15 times quicker than rendering that preview (5 s). `Report.Text()` keeps the editor's answer short: three lines.

The CLI path for a person is the same call (`cfctl audio check --spec`), and `cfctl audio check --project` measures the audio stem of a project's render when it has one.

### The CLI

`cfctl audio check --file render.mp4 --time-map time_map.json [--spec timeline.json] [--sheet sheet.png] [--json report.json]` for a file on disk, or `cfctl audio check --project prj_... --workspace wks_... [--render ren_...]` for a project's newest ready render (it downloads the MP4 and the time map from object storage and reads the planned effects from the timeline version the render was made of). Development and local use only. `--provider docker|hardened` runs ffmpeg in the media-tools image.

### What the ears cannot do

Taste: they say where and how loud, not whether it is good. Sync inside a dense mix: an effect under loud music can be present and not separable (earshot's LIMITS.md says the same of its own onset matching). Styles outside the profile: the launch-film numbers describe modern SaaS and AI launch films and music-led edits, not every good video; a talking-head edit has few onsets by nature, which is why the density checks are estimates and warnings.

## 4. Rules for the editor that come from the measurements

These are written into the sound section of [edit style v1](edit-style-v1.md#sound-p3-11). Source: earshot's launch-film profile and practice notes.

- **Density.** More sounds is not better. Sound the moments that carry meaning (the reveal, the logo, the key action, the section change) and let the music carry the rest. About one effect per 3 to 4 cuts, at most 4 per 10 s.
- **Sync.** An effect goes on its frame: a whoosh starts 0.2 to 0.6 s before the cut and peaks on it, a click is on the frame the cursor presses, an impact is on the reveal frame; a sound a frame or two off breaks the illusion. A hit on every cut is **not** what the pros do (the measured share of cuts with a hit is no better than chance in most references), dense music covers the cuts.
- **The arc.** Open with sound at frame 0, hold a steady body (the loudest 3 s sit only 1 to 3.5 LU above the programme), land the ending at body level with a short fade or a sting, no dead air. Riser, then a stop-down of 0.1 to 0.5 s, then an impact on the reveal frame: once or twice per film, not on every scene.

## 5. Verifying it

```
go test ./internal/render -run 'TestSFX|TestSoundEffects|TestLimiter|TestEveryCategory' -v    # the design, the mix, the ducking
go test ./internal/audiocheck -v                                                                # the ears, with synthetic fixtures
go run ./cmd/cfctl sfx gallery -o /tmp/cf-sfx/index.html                                         # listen
go run ./cmd/cfctl audio check --file render.mp4 --time-map time_map.json --sheet sheet.png
```

The fixtures are generated by ffmpeg when the tests run (a burst at a known time, a silence of a known length, a tone at a known loudness: EBU TECH 3341, a 1 kHz sine at -23 dBFS in both channels reads -23.0 LUFS; a clip that clips; clicks against a fake time map); nothing audio is committed. Tests run on the host's ffmpeg 7.1 and on the media-tools image's 8.1.3 (CI's pinned ffmpeg); the loudness match holds within 1 dB on both.

## 6. Sound-ready exports and ears in the editor (P3-12, [D-33](../05-decisions.md#d-33-exports-are-sound-ready))

> Status of the decision: **proposed** by the director; the founder confirms. Everything below is built so that it can be listened to, and each part can be changed on its own.

An export has no music and no trend sound (D-03: the trend sound is a cue, the preview keeps its beat guide) and must not be silent. The creator adds a trend sound in TikTok over the export, so it holds the clips' own sound as a low ambience and the generated effects on top, and speech where there is speech.

### The clip sound mode

`audio.mode` on a main-track clip is `mute` (the default: a clip with no audio settings is silent, as before), `bed` or `full`. `use_source` is the old spelling (`true` is `full`, `false` is `mute`); the two together are refused by the schema. `gain_db` is a trim on top of the mode's level. The op `set_clip_audio` takes `mode` and, **without `id`, sets every clip of the main track**, which is how the editor sets the bed policy in one op; the fields it leaves out stay as they are. The summary and the diff of a version say the mode.

### The renderer

- **Bed.** A clip in `bed` mode is trimmed, tempo-changed (a ramped clip plays at its average speed, so the bed stays continuous under a ramp; a speech clip with a ramp is still muted, `ramp_audio_muted`), set to stereo 48 kHz, faded 20 ms in and out at a cut (a crossfade fades through its transition), and set to its own level by a **per-clip evening**: `Execute` runs one `ebur128` pass over the clips (the audio part of the graph with `ametadata` files, like the `loudnorm` measuring pass), reads each clip's loudness and swaps the named `volume@bed<i>` filter for the gain that brings it to -24 LUFS (to 10 LU under the speech when the edit has speech), clamped to +-18 dB and never lifting digital silence. `Compile` stays pure: the plan holds the nominal graph and the meter arguments, and the gains are applied on the arguments only. Two clips 24 dB apart at the source have beds within 2 dB of each other (tested).
- **Sound-ready mix.** An edit with a bed and no programme sound (no clip in `full` mode, no licensed or creator audio) is **not normalised to a programme target**: the bed clips (each at about -24 LUFS by the evening) and the effects are summed and the integrated loudness simply results. The effects are mixed at their category's matched level plus `SoundReadyEffectGainDB` (4.5 dB; the category levels are matched for a programme at -14 LUFS, and the bed is far under that), so their short-term peaks land around -12 to -10 LUFS. A ceiling limiter at `SoundReadyCeilingDB` (-4.5 dBFS sample peak) closes the core, so the true peak stays at or under -2.5 dBTP after AAC (the AAC encoder adds up to about 1.8 dB on a limited hit: -3.5 read -1.7 dBTP on a synthetic test). There is no `loudnorm` and no second pass.
- **With speech.** An edit with a `full` clip is the programme as before (the programme normalisation at -14 LUFS, the effects after it, the programme ducked under accents that overlap speech); a bed clip in the same edit is 10 LU under the speech.
- **Preview.** The beat guide goes on top of the core, and the core without it is the stem (above).

### The sound-ready profile of the ears

The profile replaces the programme targets that do not apply to a bed and effects. All numbers are in `internal/audiocheck/analyse.go` and are **estimates** of ours (marked in each check's source); the sources are the design above and the earshot/practice notes.

| Check | Target | Notes |
|---|---|---|
| `loudness` (sound-ready window) | -24 to -16 LUFS (warn within 2 LU) | the web's -16..-9 window is for a programme; sound-ready: the creator's trend sound brings the programme loudness, and a bed at -24 with a few effects integrates at about -21 to -19 |
| `bed_level` | the bed's median short-term loudness within 3 LU of -24 LUFS (warn within 5) | a bed within 10 LU of a trend sound at about -14 competes with it; the evening sets each clip to -24 |
| `bed_gaps` (replaces `silent_gap`) | no silent gap over 0.5 s (warn to 1 s) | a cut fades for 20 ms; a longer hole is a clip with no sound, or one left in `mute`; the fix says `set_clip_audio` mode `bed` or an effect on that clip |
| `bed_present` | sound over -45 LUFS (momentary) for at least 90 % of the time (warn from 70 %) | an export that is silent until a sound is added feels broken |
| `bed_steady` | the quietest tenth is at most 6 LU under the median (warn to 9) | the evening makes the clips one level; a dip is a clip with little sound |
| `sfx_cut_through` | every accent peaks at least 4 LU over the bed (momentary loudness over the stretch the effect covers: from 200 ms after its `at_ms` to 400 ms after its end, so a riser is judged at its peak) | an accent is impact, sting, sub_drop, riser, downlifter, whoosh or glitch (`timeline.SFXIsAccent`); the fix names the weak effects |
| `trend_audible` | every accent peaks (momentary) at -20 LUFS or louder | the **margin against a trend sound at -14 LUFS is 6 LU**: a transient 6 dB under music is still detectable (masking; an estimate, not a listening test) |
| `sfx_sync` | at least 80 % on their frame | a hard attack (impact, sting, sub_drop, click, pop, glitch) within a frame, a whoosh or swish within 100 ms because the bed has onsets of its own; risers, downlifters and shimmers have no onset and are left to the two checks above |
| `true_peak`, `clipping`, `dc_offset`, `channel_balance` | as before | |
| `loudness_range`, `time_to_body`, `dead_air_at_end`, `ending_level`, `sound_events`, `cut_sync` | info | a bed and effects have neither a programme's range nor a body level |

### A real edit

The founder's 26 s back-day edit (`/tmp/cf-run5/spec.json`, five proxies of 4K clips from the iPhone) re-rendered with `set_clip_audio` mode `bed` on every clip, as a preview, measured on its audio stem: **-21.0 LUFS integrated, true peak -2.8 dBTP (-2.7 on the CI build); the bed at -24.2 LUFS (median of the momentary loudness, the quietest tenth 2.7 LU lower), sound for 100 % of the time, the longest silent gap 0.00 s; the three effects peak at -11.1, -9.6 and -14.5 LUFS (13.1, 14.6 and 9.7 LU over the bed), all within 6 LU of a -14 LUFS trend sound; every check passes**, on ffmpeg 7.1.1 and on the CI's static n8.1.3 build alike. Two findings came out of it and were fixed: the shot with a speed ramp had no sound, so the first run had a 3.4 s hole in the bed (a ramped clip in bed mode now plays at its average speed), and the stem was 0.4 dB hotter than the export because the limiter was after the guide mix only (the core has it now). (Those first numbers were for the version that normalised the mix to -17 LUFS: bed -18.0, integrated -17.1.) The first version of the sync check also called the whoosh 83 ms early: the nearest onset was the bed's, which is why the check is by attack type.

**Levels, changed before merge.** The first version normalised the bed and the effects together to -17 LUFS; that lifted the bed from -24 to -18, only 4 LU under a trend sound at about -14 that TikTok layers at 100 %, so the bed competed with it. The mix is now not normalised: the bed stays at -24 and the levers are `BedTargetLUFS`, `SoundReadyEffectGainDB` and `SoundReadyCeilingDB` in `internal/render/plan.go`.

### Tests

The bed compiles to the sound-ready plan in both profiles, with the limiter, the fades, the alphabet guard and the speech exception; a quiet and a loud clip 24 dB apart end within 2 dB; the cap holds; the stem has none of the guide's 1 kHz click that the preview has; a ramped bed clip keeps its sound and a ramped speech clip does not; the storage key of the stem is accepted and nothing else is; the audio profile is chosen from the spec, healthy and unhealthy sound-ready fixtures give the expected checks, with one-line fixes, and the programme profile is unchanged; `check_edit` reports the three checks, skips them with a reason without a preview, with an old preview and with a failing sampler, records the time, and measures the stem when there is a guide and the video when there is none, against a generated file and the real ffmpeg. The prompt test requires every effect category and variant of the build to be named in the prompt. All of it is run on ffmpeg 7.1 and on the CI build n8.1.3.
