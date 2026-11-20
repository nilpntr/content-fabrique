# Gino's edit rules, against what Content Fabrique enforces

> Status: **plan** · 2026-11-20 · checked against `main` at 67b5a4f. Source: Gino's three Claude skills (`video-edit`, `iphone-video-edit`, `idee-video-editor`, in Dutch, kept locally and not in the repo). Gino edits by hand with them today. Read with [style profile v1](style-profile-v1.md), [formats v1](formats-v1.md) and the AI video editor plan (`docs/design/ai-video-editor-plan.md` on `plan/ai-video-editor`).

Gino's skills hold rules he tuned by hand over many edits. Below, each rule is checked against the code (paths under `apps/server/internal/`). **Scope:** U is universal (the default for everyone), G is Gino's style (a creator setting or style profile, never a default).

## 1. The rules, and what enforces them today

| # | Rule | Content Fabrique today | Scope |
|---|---|---|---|
| 1 | **Beat grid:** 2 beats per shot, cuts on even beats, the last shot 4 beats, the total a multiple of 4. A shorter video is better than a broken rhythm. | **Partial.** `plan_edit` writes shots in beats (`agents/editor/plan.go`), `music` gives beats, downbeats and phrases, and `beat_sync`/`off_beat_cut` accept a cut on *any* beat within 2 frames for 70 % of the cuts. Nothing counts beats or bars. **Conflict:** `shot_variety` (`check.go`) fails 3 shots of the same length in a row, so Gino's grid always fails. | G |
| 2 | **Song section:** use the main section (the chorus), never the intro or a silent start. Start about 20 ms before its first downbeat, and set the phase on the kick and snare. | **Partial.** `music.FindDrop`, `hook_misses_drop` (`checkmusic.go`), and `source_offset_ms` so the drop ends the hook. Nothing stops an edit from starting in the intro. | U |
| 3 | **Song choice:** from a table (bpm, start, usable beats, verdict). Never "do not use", the song at least as long as the video, changed from edit to edit. | **Missing.** The song is whatever the creator uploads to the project (`agents/editor/music.go`). There is no library and no rotation. | G (table), U (rotation) |
| 4 | **Hard cuts only:** no fades (not on text, not at the end), no zoom, no effects. Stop hard on a strong beat. | **Partial.** The settings `transitions`, `camera_motion` and `text_animation` set to off make `effect_density` refuse effects (`check.go`). **Conflicts in the defaults:** `workout.v8` asks for the living camera and punch-ins on every shot, `minEffects` 0.15 fails a "flat" edit, and `text_without_entrance` (`checkenergy.go`) asks for a pop or fade at lively. | G |
| 5 | **Concentric only:** show the lifting part of every exercise, never the lowering part. Leg press and RDL start at the bottom. | **Missing.** Reps have a start, a turn and an end (`analysis`). `rep_boundaries` puts cuts on them, but nothing knows which half is the lift. | U (fitness) |
| 6 | **Every shot once:** no moment used twice and no action twice (a door, a lift button). A tripod clip gives one shot. | **Missing.** Only `duplicate_id` (`timeline/semantic.go`) exists, and it checks item ids, not the source ranges. | U |
| 7 | **Posing spread out:** poses never in a block (only the end shot may be a pose). Outdoor shots never back to back. | **Missing.** The `type:pose` and `type:b_roll` labels exist (`config/tags/v1.yaml`, `taxonomy`), but no check reads the order. | U, with G specifics |
| 8 | **Orientation:** all clips landscape gives a 16:9 video, uncropped. A mix gives 9:16. Never crop a clip to the other orientation. Tell the creator that a 16:9 video shows with bars. | **Partial.** The output format is the project's choice (`projects/service.go`), and the editor sees each clip's orientation (`agents/editor/text.go`). `cover` crops anything, and no check catches it. | G (rule), U (bars warning) |
| 9 | **Structure by type:** *aesthetic* has no text and follows the day: opener, the way there, the door, the gym, a quiet end shot (with lifestyle, about 1/4 on the way and 3/4 in the gym). *Exercise*: one b-roll shot, reps 1 and 2, a hard cut on a strong beat, then the last 2 or 3 reps to failure. | **Partial.** The format library has 13 cards and `format_followed` (`formats`, `checkformat.go`), but no card follows the order of the day or a set to failure. `workout.v8` ends by looping back to the hook, while Gino ends on a quiet shot. | G |
| 10 | **Text:** a hook text only when Gino gives one, in Poppins Bold, white, with a thin outline. Never over the head. Not in the bottom 20 %, not at the very top, with room on the right for the buttons. | **Enforced, except the font.** `text_safe_zone` (`timeline/safezone.go`, D-25), `text_over_subject` and `text_over_face`. Poppins is not in the font allow-list (only Anton, Inter and Montserrat, in `timeline/style`). | U (zones), G (font) |
| 11 | **First shot centred:** the subject is centred in the first shot, because Instagram's grid shows the middle. | **Missing.** | U |
| 12 | **Colour:** iPhone footage keeps its own colours. HDR goes to SDR, bright shots are darkened a little and dark ones are never lifted. DJI D-Log M footage gets a LUT and one look. | **Enforced for iPhone.** HLG and PQ are tone-mapped (`media/derivatives.go`), `match_exposure` only darkens (`matchexposure.go`), and there are the looks `none`, `auto` and `gym_moody`. **Missing:** a LUT path for D-Log footage. | U (HDR), G (looks) |
| 13 | **Loudness:** -14 LUFS, the audio cut to the video's length, music only. | **Enforced.** `render/audio.go`, the evals' `music_only` detector, D-34. | U |
| 14 | **Clip hygiene:** never screen recordings, duplicate exports, other recognisable people, a middle finger, or a "bad shot" from the catalogue. | **Partial.** The `exclude`, `include` and `prefer` comments hold for every revision of *one project* (`annotations/service.go`, `agents/editor/comments.go`). There is no blocklist per clip across projects and no flag for other people. | U |
| 15 | **Proof edit:** every new edit and every feedback version gets a shot number at the top right, with a shot list (number, clip, second). Feedback such as "never use shot 5 again" is stored by clip and second (numbers change). The final render, without numbers, comes when Gino asks for it. | **Partial.** The web's plan card numbers the shots with beat pips (`apps/web/.../run-plan-card.tsx`), and comments resolve to items through the time map. The numbers are not burned into the preview, and no feedback outlives the project. | U |
| 16 | **Feedback becomes a rule:** written down with its date, in the style card for that type. | **Partial.** `suites/feedback.yaml` and its detectors (`evals/feedback.go`) are the founder's and used in evals only. `creatorstyle` learns settings, not dated rules. | U |
| 17 | **Colour-blind safe:** never red with green. | **Partial.** The check chips have an icon and a label, but pass and fail are `#0ca30c` and `#d03b3b` (`apps/web/src/index.css`). | U |
| 18 | **Idea inbox:** spoken ideas go into a document (Inbox, In progress, Done). | Out of the product. It is Gino's workflow, and the Claude Doc works for it. | G |

## 2. Fix the conflicts first

Even with Gino's settings in place, rules 1 and 4 fail today: `shot_variety` fails his grid, and `workout.v8` with `minEffects` pushes motion he never uses. Before adding any check:

- Skip `shot_variety` and `pace_builds` when the creator's cut grid is fixed (a new setting, `cut_grid: fixed`).
- Set `minEffects` to 0 when `camera_motion` is off.
- Skip `text_without_entrance` when `text_animation` is off.
- Make the playbook's "living camera" and "loop back to the hook" lines depend on those settings.

All of this is deterministic and costs $0.

## 3. What is missing, ranked by value

1. **Bar arithmetic:** a `beat_grid_fixed` check under a `cut_grid: fixed_2` setting. Every shot is a whole, even number of beats, cut on an even beat, the last shot 4 beats and the total a multiple of 4. `plan_edit` refuses a plan that breaks this. (Rule 1.)
2. **Proof edit and clip-level feedback:** a `proof` render flag that burns in the shot numbers (`drawtext`, small, at the top right) and a shot list in the run feed. A "never again" or "start 2 s later" on a shot becomes a stored `asset_feedback` row (asset, source ms, kind) that `find_moments` and the validator read in *every* project. (Rules 14 and 15.)
3. **Every shot once:** a `shot_reused` check. It fails when two items overlap in the source range of one asset, or when one tripod clip (camera `static` from `camshift`) is used twice. Add a playbook line about the same action twice. (Rule 6.)
4. **Concentric only:** the analysis marks each half of a rep as `up` or `down` (from the sign of the motion between the start and the turn). Then a `concentric_shown` check requires every exercise shot to contain an `up` half, and the workout playbook gets a line about it. (Rule 5.)
5. **Orientation rule:** suggest the project's output format from its clips' orientations (all landscape gives 16:9). A `cross_crop` check fails a landscape clip cropped with `cover` into 9:16 unless the creator allows it. Show "16:9 plays with bars" in the UI. This complements P3 (subject-aware reframing): P3 makes crops good, and this rule says when not to crop. (Rule 8.)
6. **Posing spread:** a `pose_block` check. Two `type:pose` shots in a row fail, except for the ending, and so do two outdoor b-roll shots in a row. (Rule 7.)
7. **Skip the intro:** a `song_intro` check. The song must start at or after the first phrase (`PhrasesMs`) with a loud downbeat, unless the creator says "from the start". (Rule 2.)
8. **Day order and set-to-failure formats:** two format cards. `day_in_order` keeps the clips in recorded time order, opener to a quiet end. `set_to_failure` has a b-roll shot, the first 2 reps, then the last 3 reps. (Rule 9.)
9. **First shot centred:** a `hook_centred` check. The subject box of the first shot (`subject.MapBox`) has its centre within the middle third. (Rule 11.)
10. **Colour-blind palette:** blue and orange pass and fail tokens, with the icon and the label kept. (Rule 17.)
11. **A song library with rotation:** the workspace's songs with their bpm and a verdict. `plan_edit` advises against the last 3 songs used. (Rule 3.)
12. **Poppins and D-Log:** add Poppins Bold to the allow-list (a D-16 licence check: it is under OFL, so fine), and add a `lut` look for D-Log M footage, which needs a design note on staging the `.cube` files. (Rules 10 and 12.)

## 4. Personal style or universal

- **Gino's style (settings, never defaults):**
  - the fixed 2-beat grid and the 4-beat ending (1)
  - no motion, no fades and hard cuts only (4). This goes against the founder's "vibey, not static" default, and that is why it has to be a setting.
  - landscape kept as 16:9 (8)
  - the structures by type and the quiet ending (9)
  - Poppins Bold (10)
  - the DJI looks (12)
  - the song table (3)
- **Universal:**
  - every shot once (6)
  - poses spread out (7)
  - the concentric part (5, for fitness)
  - skipping the intro and rotating songs (2, 3)
  - the first shot centred (11)
  - the safe zones, HDR and loudness (10, 12, 13)
  - the clip blocklist, the proof edit with shot numbers, and feedback that is stored (14, 15, 16)
  - colour-blind safe UI (17)

## 5. Where this fits in the plan

The plan's five pieces (P1 cutting on speech, P2 captions v2, P3 subject-aware reframing, P4 a b-roll overlay track, P5 brand kit) are about speech-led videos and branding. Gino's rules are about music-led workout montages. The only overlaps are item 5 here, which sits next to P3, and the Poppins font in item 12, which P5's font choice will use. **Run this as its own track, before P1:**

- Section 2 and items 1, 3, 6 and 9 are pure Go checks on footage we already have (rung 0, $0), and they decide whether Gino's first edits feel like his own.
- Items 2 and 4 need a small analysis change.
- Item 2 also needs a web view.
- Items 8 and 11 are config and prompt work.

Proof: record Gino's settings, replay the gym cases with `cfctl eval suite`, and turn his feedback into `suites/feedback.yaml` items with detectors (one per check above).
