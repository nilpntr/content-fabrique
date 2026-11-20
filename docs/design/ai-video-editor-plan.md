# AI video editor: the next five pieces

> Status: **plan** · 2026-11-20 · checked against `main` at 67b5a4f. Read with [timeline v1](timeline-v1.md), [edit style v1](edit-style-v1.md), [style profile v1](style-profile-v1.md), [D-02](../05-decisions.md#d-02-edit-representation-declarative-timeline-json--deterministic-renderer), [D-28](../05-decisions.md#d-28-content-aware-editing-across-genres), [D-38](../05-decisions.md#d-38-text-follows-the-creators-pattern-lines-on-the-body-keep-off-the-face-only).

Goal: make the editor do what a human short-form editor does: auto-cut, captions, hook and pacing, b-roll and overlays, vertical reframing and the creator's brand. Most of the picture side is built and tuned on the founder's gym footage. **The biggest hole is speech:** we transcribe clips, but the editor never reads the transcript, so talking, tutorial and product videos (D-28) get cut by eye.

## 1. What exists, and what is missing

All paths are under `apps/server/internal/`.

| Capability | Exists | Gaps |
|---|---|---|
| **Auto-cut** | `analysis`: shots (`scdet`), reps, holds, dead time, `rep_candidate`. `find_moments` (`agents/editor/moments.go`). `check_edit` checks rep boundaries and dead time (`agents/editor/check.go`). Beat snapping in `timeline/beats`. `exclude` comments are enforced by the validator. | No cutting on speech. `transcribe/structure.go` already stores sentences, filler words and pauses in `transcripts.structure`, but there is no editor tool for them. `GetTranscript` is used only by `renderjobs` for captions. `prompts/playbooks/talking.v4.md` line 14 is a TODO for exactly this. |
| **Captions** | `CaptionsTrack` built from the transcript (`timeline/types.go`), with styles `chunk` and `word_highlight` (`timeline/style/registry.go`). Words are mapped through trims and speed changes (`render/text.go` `mapWordsOf`), drawn as ASS (`render/ass.go`), and the caption band is checked against the safe zone (`timeline/captions.go`). There is a `set_captions` op. | The band sits at a fixed 62 % of the safe zone (`timeline/style/layout.go` `captionCentre`), whatever the subject is doing. `textsubject.go` checks text items against the face and body, but not captions. Captions ignore the creator's settings (colour, case). The creator can't correct a misheard word. There are only two looks. |
| **Hook and pacing** | `plan_edit` writes the hook, arc, ending and shots in beats (`plan.go`). Pace comes from the style profile and the vibe (`pace.go`). Checks: `hook_length` (stretched to the drop), `hook_title`, `shot_pacing`, `long_shot`, `held_ending`, `pace_builds`, `beat_sync`, plus the energy checks (`checkenergy.go`). Also the format library (`formats`, D-40) and the drop detection (`music`). | The hook is chosen from the picture only. A talking video can't open on its best line. Pacing knows shot lengths but not speech: no words per shot, no check that a pause was left in. |
| **B-roll and overlays** | A `graphics` track with 7 HTML templates (`motion/bundle/templates`, D-35), placed off the subject (`timeline/avoid.go`, `subject/place.go`) and checked by `check_graphics.go`. Text overlays. A `type:b_roll` label (`config/tags/v1.yaml`, `taxonomy`). | Exactly one video track (timeline v1 §2 says "overlay video tracks come later"). A b-roll shot placed on `main` replaces the speech audio under it, so jump cuts can't be covered. There's no picture-in-picture and no image or logo overlay. |
| **Vertical reframing** | `Reframe` modes `cover`, `contain` and `blur_fill`, with a focus point and zoom (`timeline/types.go`, `render/video.go`). Camera moves (`render/camera.go`). `set_reframe`. Subject boxes from the quick look, 4 frames per clip (`quicklook`, `subject`). `subject.MapBox` maps a box through a reframe. `camshift` measures camera motion. | The model sets the focus by eye (prompt `editor.v2.11.md` line 116). Nothing fills it in from the subject boxes, and nothing checks that the person stays inside the crop. The focus is fixed per clip, and keyframed motion is out of scope in timeline v1. 4 samples per clip are too few for a subject that moves. |
| **Brand style** | `styleprofile` (measured from reference videos), `creatorstyle` (jobs and `/v1/style`), and the creator's settings (title colour, case and position, pace, text pattern, ...) under D-36's three layers. `style_match` and `title_copied` checks. Per-item text overrides (`timeline/style/overrides.go`). A font allow-list. | `creator_profiles.brand` (jsonb, migration 00002) is stored but nothing reads it. There's no logo or watermark, no palette for captions or graphics accents, and no branded outro (nothing feeds a brand into `outro_card`). |

## 2. The five pieces, in order of value

### P1. Cutting on speech: jump cuts and a spoken hook

- **Why:** this unlocks every speech-led genre in D-28. Captions (P2) and b-roll covering (P4) build on it. The data is already in the database.
- **Scope:**
  - A pure-Go planner, `internal/speechcut`. From `transcribe.Structure` and a source range it returns the ranges to keep: fillers and pauses over about 400 ms removed, cuts on word gaps with 60 to 100 ms of padding, sentences kept whole.
  - An editor tool, `get_transcript(asset_id, in_ms?, out_ms?)`. It returns the sentences with their times and the proposed keep ranges, with the transcript in data blocks as untrusted text.
  - The model still writes `add_clip` ops (D-02 unchanged: no new op, no schema change).
  - New `check_edit` checks: `speech_cut_clean` (no cut inside a word, no filler kept, no long pause kept) and `hook_line` (a speech-led edit opens on a sentence and not in the middle of one).
  - The `talking`, `tutorial` and `unboxing_product` playbooks move to v5, and the prompt to `editor.v2.12`.
- **Packages:** `transcribe` (read only), `store` (existing `GetTranscript`), new `speechcut`, `agents/editor` (`tools.go`, `check.go`, `plan.go`, prompts), `evals` (a talking case in `suites/`).
- **Acceptance:**
  - On a fixture transcript, the planner drops every filler and every pause over 400 ms, and never cuts inside a word (table tests).
  - On a real talking clip, the edit has no pause over 400 ms and opens on a whole sentence.
  - The captions stay in sync within one frame, which `mapWordsOf` already does across cuts.
  - Existing gym cases score the same in `cfctl eval suite`.
- **Cheap proof:**
  - Rung 0: planner and check tests on synthetic `Word` slices, and editor cassettes re-recorded on `dev-cheap` (about $0.05).
  - whisper.cpp runs locally for free (D-27). `cfctl render` renders the result locally.
- **API cost:** about $0.60 per live Opus edit (medium effort, FOUNDER-PLAN). A 3-run smoke costs about $2.

### P2. Captions v2: off the body, in the creator's look, correctable

- **Why:** captions are the main text of a talking video. The fixed band breaks the founder's "text never on the body" and D-38's "keep off the face".
- **Scope:**
  - The band is placed per clip from the subject boxes: the upper or the lower band, whichever is clear of the face (and of the body for titles). It uses the `subject.MapBox` path that text already uses.
  - A `caption_over_face` check.
  - The caption fill and case come from the creator's settings, with D-36 precedence.
  - `corrections` on the captions track (word index to replacement, cleaned with `textclean`, length-capped) and a UI action to fix a word. This is an additive schema field.
  - One more look: keyword emphasis in the creator's accent colour.
- **Packages:** `timeline` (`captions.go`, `types.go`, schema, ops), `timeline/style`, `render` (`text.go`, `ass.go`), `subject`, `agents/editor` (`textsubject.go`), `apps/web` (caption edit), `packages/schemas`.
- **Acceptance:**
  - A golden render of a talking clip has no caption over the face box in any format.
  - A correction shows in the next render without a model call.
  - Hostile corrections are refused by the schema, the op and the compiler.
- **Cheap proof:** fully deterministic. Golden tests use synthetic subjects (`cfctl render --subjects`). **$0**.

### P3. Subject-aware reframing

- **Why:** landscape and 4:5 sources must work in 9:16 (D-25). Today one wrong guess from the model crops the person out, and nothing notices.
- **Scope, part A (no schema change):**
  - When the model leaves `focus` out, the focus defaults to the subject's centre: the median over the subject samples inside the clip's source range.
  - A `subject_in_crop` check: the mapped box must stay at least 90 % inside the output frame. When a crop can't hold it, the check suggests `blur_fill`.
- **Scope, part B (design note first):**
  - A `focus_path` on a reframe: up to 8 keyframes, eased, rendered through the perspective path in `camera.go`.
  - Denser subject samples, up to 12 per clip.
- **Packages:** `subject`, `quicklook` (more samples), `timeline` (part B only), `render` (`video.go`, `camera.go`), `agents/editor` (`check.go`, `formats.go`).
- **Acceptance:**
  - A 16:9 clip with the subject at x = 0.25 comes out in 9:16 with the subject inside the crop, both without a focus and with a wrong one (the check fails, and the fix passes).
  - Part B: a subject walking across the frame stays within the centre 60 %, and the golden tests hold.
- **Cheap proof:** synthetic subject JSON and golden renders cost $0. Re-reading the subjects of 20 clips at 12 frames costs about $0.20 (Haiku). One live 16:9 edit costs about $0.60.

### P4. B-roll cutaways on an overlay track

- **Why:** an editor covers jump cuts and illustrates the words with b-roll while the speech keeps playing. That isn't possible with a single video track.
- **Scope:**
  - A new `overlay` video track, additive inside v1 as timeline v1 §8 allows. Items are picture only (`asset_id`, `source`, `at_ms`, `reframe`, `mode`: `full` or `pip` with a corner and a scale), with the audio always from `main`.
  - Ops: `add_overlay`, `trim_overlay`, `remove_overlay`.
  - The compiler adds an `overlay` filter on top of `main`, under the text and graphics.
  - The time map reports both items, so a comment resolves to the cutaway.
  - The editor gets b-roll candidates (`type:b_roll` and `product`) next to the speech cuts from P1. A `jump_cut_covered` check is advisory.
- **Packages:** `timeline` (types, schema, ops, diff, validate), `render` (`compile.go`, `plan.go`, `video.go`), `renderjobs` (staging of overlay assets), `annotations` (time map), `agents/editor`, `packages/schemas`, `apps/web` (the track view).
- **Acceptance:**
  - The speech audio is unchanged under a cutaway (PCM hash test).
  - The overlay renders in all 4 formats (golden tests).
  - A comment on a cutaway resolves to the overlay item.
  - The validator refuses an overlay outside the timeline or longer than its source.
- **Cheap proof:** schema and fuzz tests, golden renders and replay cost $0. Two live edits on a talking clip plus b-roll cost about $1.20.

### P5. Brand kit

- **Why:** this is the cheapest piece, and agencies and creators expect it. The column already exists.
- **Scope:**
  - Give `creator_profiles.brand` a typed, validated shape: a palette (3 hex colours), a title and caption font from the allow-list (no font uploads, which keeps D-16 and the allow-list intact), an optional logo asset (an image, `purpose: brand`), and the logo's corner and opacity.
  - D-36 precedence: the creator's style settings win over the brand.
  - The compiler adds the logo as a corner overlay and applies the brand colours to captions, the `accent` of graphics, and `outro_card`.
  - The editor sees the brand in `get_brief`.
- **Packages:** `store` (no migration, the jsonb exists), `projects` or `creatorstyle` (handler), `timeline/style`, `render`, `motion` (template params), `agents/editor`, `apps/web` (Settings → Brand).
- **Acceptance:**
  - A set brand changes the caption, graphic and outro colours and adds the logo, in golden tests.
  - An empty brand renders byte-identically to today.
  - Brand values are refused by the same rules as overrides (hex only, allow-listed fonts).
- **Cheap proof:** deterministic, **$0**.

**Total live spend for all five:** about $6 to $8 on Opus, well inside the $75 test cap. Everything else runs on rung 0 or 1 of [D-22](../05-decisions.md#d-22-cheap-first-testing-ladder).

## 3. Risks, and what needs a design note

- **A design note is needed first for P4**, the overlay track. It is the first change to timeline v1's "exactly one video track" rule. It touches the schema, the compiler graph, the time map, comment resolution and the web track view, so it must settle the audio ownership, the z-order against `graphics` and text, and how PiP sits in the safe zones. **P3 part B** (`focus_path`, keyframed motion is out of scope in timeline v1 §8) needs a short section in the same note. P1, P2, P3 part A and P5 can start without one.
- **Footage:** P1, P2 and P4 need a consented talking clip, and the founder's unboxing or talking footage is still pending (FOUNDER-PLAN §1). Until it arrives, proofs use synthetic transcripts and the eval cases stay placeholders.
- **Transcript quality:** whisper's word times drift on music and gym noise. The planner must pad its cuts and refuse to cut where the word confidence is low, and the error budget (< 100 ms median, P2-05) has to be measured on real speech.
- **Subject data:** boxes come from Haiku reading 4 frames. They are untrusted, sparse and cost money to densify. D-28 names MediaPipe face detection as the candidate. **Flag, not an override:** the code went with model boxes. Part B should record the choice (MediaPipe after a D-16 licence check, or more quick-look frames) as an amendment to D-28.
- **Founder preferences:** "music only" (D-34) holds for workout edits. Speech-led edits keep `audio.mode: full` with the song ducked, which `render/audio.go` already does. The P1 playbooks must not carry the music-only rule over to talking videos.
- **Scope creep:** auto-cutting must stay within the creator's selected clips. Long-form → shorts clipping is a v1 non-goal (product plan §12), and so is generated b-roll (open question 6).
