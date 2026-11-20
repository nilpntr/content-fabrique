# Design note: b-roll cutaways on a second video track

> Status: **proposed** · 2026-11-20 · checked against `main` at f2ddb38. Piece 4 of the [AI video editor plan](https://github.com/nilpntr/content-fabrique/blob/plan/ai-video-editor/docs/design/ai-video-editor-plan.md). Amends [timeline v1](timeline-v1.md) §2 and §8 (the first change to "exactly one video track"); the PR that ships the schema edits that note too. Decisions are in [05-decisions](../05-decisions.md): D-02, D-22, D-28, D-34, D-37, D-38.
>
> **Slice 1 built** (render core): `timeline.BrollTrack`, `broll_check.go`, `render/broll.go`, fixtures `v52`-`v55` (one per format, `render-broll.txt`) and `i89`-`i98`. Found on the way: the rendered PCM of an edit with a song ducked under speech is not reproducible run to run on ffmpeg 7.1 even without cutaways (multi-threaded filter graph), so its proof is the byte-identical audio plan; the four b-roll fixtures are speech-only, and their PCM hash is equal with and without the track in both profiles.

**Goal.** Show another picture over a speech clip while the speech keeps playing, so jump cuts from P1 can be covered and words illustrated. Full-frame cutaways only; picture-in-picture is not in this note (section 9).

## 1. Schema

- A new track type **`broll`** (id `broll`, at most one, optional). `main` stays the only track of type `video`, so that rule stays literally true and the schema constant stays `contentfabrique.timeline/v1`: a new track type is additive (timeline v1 §8).
- Item: `id`, `asset_id`, `source {in_ms, out_ms}`, `at_ms` (required); `reframe`, `look`, `why` (optional, as on a clip). **No** `audio`, `speed`, `speed_ramp`, `motion` or `transition_in`: the schema is closed, so they are refused. Its length is the source span; it cuts in and out (D-37).
- **No migration.** `timeline_versions` is append-only and old specs stay valid. Every new validator rule fires only when a `broll` track exists, because render jobs re-validate stored versions (`renderjobs/pipeline.go`). Acceptance: golden plans of every existing fixture are byte-identical.
- Forward only: a spec with the track is invalid on an older binary. Deploy the server before the feature is on; rollback is the feature flag, not a schema revert.
- Ops (`ops.v1`, additive): `add_broll`, `trim_broll`, `remove_broll`; `move_item` and `set_reframe` accept its ids; `set_look` without an id also grades cutaways (one grade per video).

## 2. Speech audio under a cutaway

The audio graph reads `main` only and is not touched. A cutaway's input is opened with `-an` and no audio pad is ever mapped from it, so the programme, the song ducking, the effect ducking and both `loudnorm` passes are the same with and without the track. Captions keep following the words of `main` (`mapWordsOf` unchanged). Test: the plan's audio statements are byte-identical and the rendered PCM hash is equal, in both profiles.

## 3. Validator

| Code | Rule (error unless noted) |
|---|---|
| `outside_timeline` | `at_ms + length` at most the total duration |
| `source_out_of_range`, `unknown_asset`, `wrong_asset_kind` | as for a clip; the asset is a video |
| `shot_too_short` | at least 300 ms, counted in frames at the spec's fps |
| `overlap` | two cutaways share a frame |
| `source_reused` (new) | its source range overlaps the range of any other item, on `main` or `broll`, of the same asset |
| `too_many_cutaways` (new) | more than 12 items (each is an ffmpeg input and about 400 bytes of graph, under `MaxFilterGraphBytes`) |
| `cutaway_over_silence` (new, warning) | no `main` clip under it has `audio.mode: full` |

**Ripple.** A cutaway belongs to the words under it, so when an op changes the length of `main` it moves with the clip under its start; text and graphics keep their absolute times as today. One left past the end is `outside_timeline`.

## 4. Renderer

One more stage in `videoGraph`: join, **cutaways**, motion graphics, ASS. Z-order from the bottom: `main`, `broll`, `graphics`, text and captions, watermark. Each item is an ordinary seeked input (in `c.inputs`, so it sits before the motion image sequences) and reuses `clipGraph`'s chain for exact frame counts:

```
-ss 12.400 -t 1.800 -an -i <asset>
[k:v]trim=end=1.8,setpts=PTS-STARTPTS,fps=30,tpad=stop=-1:stop_mode=clone,trim=end_frame=54,
     <reframe>,<look>,format=yuv420p,settb=AVTB,setpts=PTS+<start_frame/fps>/TB[b0]
[vj][b0]overlay=0:0:eof_action=pass:repeatlast=0[vb0]      (then [vb0] to graphics, then ass, then [vout])
```

This is the pattern `overlayGraph` already uses for motion graphics (shifted PTS, pass-through outside the segment). In Go the names are `broll*`/`cutaway*`: `overlay` already means motion graphics in `render` and in the checks.

- **Time map:** `main` entries are unchanged, so every output time still resolves to exactly one main item. Cutaways are extra entries with `track: "broll"`, the asset and `source_in_ms`. `annotations.Resolve` returns both spans, the cutaway first, and the editor is told which one is on screen.
- **Text and the subject:** where a cutaway is on screen, `PlaceTextAvoiding`, the caption band and `motion.Layout` read the subject boxes of the cutaway's asset, not of the clip under it (founder: text never on the body; D-38).
- **Contact sheet:** one tile per cutaway, 100 ms after it starts, inside the 60-tile cap.

## 5. How the editor picks cutaways

A tool `find_broll(at_ms?, limit?)`, server-side and deterministic:
- **Candidates:** the project's own clips tagged `type:b_roll` or `type:product` (`taxonomy`), and rep and hold moments (`find_moments`) of other clips. Ranked by tag fit, then by the D-30 hybrid search with the sentence under `at_ms` as the query. The server builds the query; the transcript stays untrusted data.
- **Never a used range:** ranges already on any track of the current version are subtracted before ranking, as are dead time and `exclude` comments. The validator's `source_reused` is the backstop.
- **Exercises:** candidates are whole reps on their boundaries. See flag 3 for concentric-only.
- The model writes `add_broll` ops. Only the creator's selected clips: no stock or generated b-roll (plan §3).

## 6. Checks (`check_edit`)

- `jump_cut_covered` (advisory): a speech cut with a visible jump and no cutaway over it, when candidates exist.
- `cutaway_length`: 1 to 4 s, whole beats when the edit has a song.
- `cutaway_on_hook`: the first sentence shows the speaker.
- `cutaway_coverage`: at most 40 % of the speech time.
- Existing checks read the visible picture: `shot_pacing`, `long_shot`, `static_shot` and `beat_sync` count cutaway edges as cuts; `brightness_jump` and `crushed` sample cutaway frames; `text_over_subject` and `text_over_face` use the visible layer.

## 7. UI

- `review-timeline.tsx`: a second lane above the clips, shown only when the track exists, labelled "B-roll" (no track jargon). `time-map.ts` returns the topmost picture at `t`; a comment made on a cutaway pins to it, and "remove this" removes the cutaway, not the clip.
- Version summaries: "added b-roll b1 (plates, 2.0 to 3.8 s) over c3". No manual drag yet; the creator changes cutaways by comment.

## 8. Rollout and tests

| Slice | Contents | Proof (D-22) |
|---|---|---|
| **1. Render core (first PR)** | Schema, TS and Go types, the validator rules, the compiler stage, time-map entries, the timeline v1 amendment. No ops, no agent, no UI: only fixtures and `cfctl render` can make one | Rung 0, $0: a valid fixture per format, an invalid one per code, fuzz, golden plans and frames, PCM hash, caption sync, existing goldens unchanged |
| 2. Ops and review | The three ops, ripple, diff and summaries, `Resolve`, the web lane, the contact-sheet tile | Rung 0: property tests on random op batches, `Resolve` and web unit tests |
| 3. Editor | `find_broll`, the checks, prompt `editor.v2.13` and playbooks behind a feature (`.Has "op:add_broll"`), a talking eval case | `LLM_MODE=replay` cassettes re-recorded on `dev-cheap` (about $0.05); gym cases score the same; then 2 live edits (about $1.20) once consented talking footage exists |

## 9. Flags: contradictions and gaps (not overridden here)

1. **Timeline v1** says exactly one video track and lists overlay tracks as out of scope. This note amends it; that needs the founder's or director's sign-off before slice 1 merges.
2. **The plan names the track `overlay` with a `pip` mode.** Proposed instead: the name `broll` (the clash above) and no PiP yet. PiP needs its own safe-zone and caption-band rules, and the first creator's rules say no effects. Confirm or keep the plan's name.
3. **Concentric-only is not buildable today.** No style setting holds it and the analysis knows a rep's turning point (`peak_ms`) but not which half is the lift. It needs a "Your style" setting (D-36) and a direction per exercise. Until then cutaways use whole reps.
4. **"Each shot used once"** is enforced here for cutaways only. Making it an error on `main` would fail stored versions at render time; proposed as a `check_edit` advisory instead. Founder's call.
5. **Music-only edits (D-34):** with no speech, a cutaway is the same as a clip on `main`. The warning and the prompt keep the track for speech-led edits; workout edits do not change.
6. **The ripple rule** differs from text and graphics (absolute times). It is a new rule in timeline v1 §4, not a change to theirs.
7. **Footage:** no consented talking clip exists yet (FOUNDER-PLAN §1), so slice 3's live proof and eval case wait; slices 1 and 2 do not.
