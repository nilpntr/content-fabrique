# Editor system prompt: changelog

The editor agent's system prompt is `editor.v2.1.md`, embedded in the Go binary, and the genre playbooks are the files in
`playbooks/`, embedded too. The text the model receives is the runtime's preamble (`runtime.SystemPreamble`, the untrusted-data
rule) followed by this file. The prompt version (`PromptVersion` in `prompt.go`) is stored in every run's input, so a run can
always be tied to the text it was given; a playbook has its own version (`workout.v2`, in `playbooks.go`) that the run's plan
records.

Both the prompt and the playbooks are Go templates over the features of the build (`features.go`): the sections about an effect,
a look or a sound effect sit behind `{{if .Has "..."}}` and are cut out unless the embedded op schema has that op or preset. So the
prompt never offers an op that does not exist, and when part B of P3-10 (looks and sound effects) merges, its sections turn on with
no change to the text.

Rules for changing it:

- A change to the text is a new entry here and, when it changes behaviour on purpose, a new `PromptVersion` (a new file, such as
  `editor.v1.5.md`). A typo fix or a clarification that changes nothing keeps the version and says so here.
- The prompt is part of every request's cache key, so any change starts a cold prompt cache (one cache write, then reads).
  Record mode cassettes depend on it too: after a change, re-record with
  `CF_UPDATE_CASSETTES=1 go test ./internal/agents/editor -run TestEditorCassettesAreCurrent`.
- The cache hit rate of a run is visible: `cfctl agent show <run>` prints the cache write and read tokens of every model call and
  the hit rate of the run (cache read tokens over all prompt tokens); `GET /v1/runs/{id}` returns the same totals.
- The benchmark (P4-05) and the live runs on the founder's footage decide whether a version stays; note the outcome here.

## editor.v2.1 (2026-10-17)

Sound-ready exports and ears in the editor (P3-12, decision D-33 in `docs/05-decisions.md`, proposed by the director and to be
confirmed by the founder). The prompt was written when the editor had no sound tools: it left the clips silent, kept `gain_db` off and
knew four effects. Since then P3-11 added the other seven effect categories and variants and the audio check, and P3-12 added the
clip sound modes and the sound-ready mix, so the editor is taught all of it. The file is now `editor.v2.1.md`; it changes what the
editor puts on the timeline (a bed under every shot), so it is a new version, a minor one because the process is the same.

- **The export is sound-ready.** The Sound section is rewritten: the creator adds a trending sound in TikTok over the export, so the
  export is not silent and does not fight it. A licensed trend sound is a cue, never exported; no music. Each clip has a sound mode
  (`set_clip_audio`, new field `mode`): `mute`, `bed` (a low ambience at about -24 LUFS, evened out across the clips, short fades at
  the cuts, never normalised up) and `full` (the programme, for speech, ducked under effects). The policy is one op with no id,
  `{"op":"set_clip_audio","mode":"bed"}`, when nobody speaks; a clip with speech is `full` and the rest stay bed; `full` is never for
  ambience. The mix is not normalised to a programme target: the bed stays at about -24 LUFS and the effects peak around -12 to -10 LUFS, so a trend sound at about -14 dominates the bed and the effects still cut through it; the export integrates at about -21 to -19 LUFS for few effects.
- **Sound effects.** The effects bullet teaches all eleven categories (whoosh, swish, impact, riser and now downlifter, sub_drop, pop,
  click, glitch, shimmer, sting) with their variants and what each is for, `variant` (omit it for the first, `"auto"` to vary by the
  effect's id), the rule of one effect sound per visual effect and none on a plain cut, and that `gain_db` is left alone unless
  `check_edit` says an effect is lost in the bed. A test checks that every category and variant name of the build is in the prompt.
- **Loudness numbers are scoped.** The levels (bed about -24, export about -21, effects -12 to -10) are for an edit with no `full` clip; an edit with speech is the programme at -14 LUFS with the bed 10 LU under it. The prompt tells the editor to trust the target in each check's detail and never to switch speech to bed (or bed to full) because of a loudness number.
- **Rhythm.** "You have no sound tools" is gone: the beat grid is still written, now so that the cuts and effects match the sound the
  creator adds.
- **check_edit listens.** Step 5 and "Checking and finishing" describe the three new checks (`audio_level`, `audio_bed`,
  `audio_effects`, measured on the rendered preview without its beat guide) and what to do about each: a missing bed (bed policy, or an
  effect on a clip with no sound), an effect lost in the bed (a few dB, or a heavier variant), a bed that is not at about -24 LUFS or an export outside -24 to -16 LUFS. A "borderline"
  note is an estimate close to its bar and not a reason to revise.
- **Playbooks** are all `v2`: workout (bed on, a whoosh on whips, a sub_drop with the impact on the drop, a riser into the hook, a sting
  on the loop), unboxing (the reveal's own sound kept as `full`, the rest a bed, a sting or impact on the reveal, a pop and a click),
  talking (`full` on speech, a bed on cutaways), general, vlog and tutorial (bed and full instead of `use_source`).
- Cassettes re-recorded (the prompt, the ops schema and the tool contract changed). No live model call was made for this change.

## editor.v2.0 (2026-10-17)

Editor v2 (steps 3 and 4 of `docs/design/edit-style-v1.md`, and the measurement slice of step 5). The first live Opus edit of real
gym footage after clip analysis had every cut on a rep boundary and no dead time on screen, but the edit was flat: shots of 4.5 to
5 s (one slow rep each), only hard cuts, no beat grid, a static title and no check of its own work. The file is now
`editor.v2.0.md`; it changes behaviour a lot, so it is a major version.

- **Understand, then plan.** New step 3: before the first edit the editor calls `plan_edit` (new tool): content type, story, hook,
  arc, ending, playbook, target length, tempo and a shot list in beats. The answer carries the playbook of the chosen genre, the
  length of a beat and what the shot list adds up to. The first `edit_timeline` of a run is refused without a plan. The plan is an
  artifact event (`creative_plan`) for the run feed.
- **Playbooks** (new, `playbooks/*.v1.md`): talking, unboxing_product, workout, vlog, tutorial and general, from edit-style-v1
  sections 2 and 3. Only the chosen one enters the conversation, as the result of `plan_edit`, so the system prompt and the tools
  (the cached prefix) are the same for every run and project. The run context may carry a one-line guess of the content type.
- **Rhythm.** New section "Pacing and rhythm": a default beat grid of 120 to 128 BPM (the beat length in ms is in the prompt), shot
  lengths in beats (hype shots of 1 to 2 beats, a full rep only where it is worth it), a long rep trimmed to its strongest half or
  sped up (`set_speed`, a speed ramp), cuts that speed up toward the end, never more than two equal-length shots in a row, and the
  half-way turning point of a rep as a legal cut.
- **Effects with restraint** (new section, each part behind its feature): `set_motion` (punch_in on a beat hit, push_in, shake),
  `set_speed_ramp` (fast into slow at the peak, on a few shots at most), `set_transition` (whip_left, whip_right, zoom_through,
  flash) with examples, about one effect per 3 or 4 cuts. Looks (`auto`, `gym_moody`: one grade for all clips, never `exposure_ev` or
  `match`) and sound effects (`add_sfx`: a whoosh on a whip, an impact on the drop, a riser into a big moment, one per effect) were
  written ahead of part B of P3-10 behind their features and turned on when it merged.
- **The order of the ops** (new): everything that changes a shot's length (speed, transitions, ramps) first, then
  `snap_cuts_to_beats` once with `write_markers` and `max_shift_ms` 80, then what does not change timing (motion, text, audio, looks). `snap_cuts_to_beats`
  cannot move a cut next to a ramped clip, so a ramped shot is tuned to whole beats before the snap.
- **Self-review.** Step 5 now ends with `check_edit` (new tool: objective checks computed from the timeline and the analysis), and
  step 6 is one revision pass at most. `finish` is refused until the newest version has a preview and a `check_edit`.
- **Exposure** paragraph rewritten: the analysis line now gives the p90 luma and says "low-key" for footage that is dark overall;
  low-key is fine and never to be brightened. Only the jump at a cut matters, and `check_edit` measures it. The "no automatic
  exposure correction" line is gone with the gain wording.
- The result of `edit_timeline` and `get_timeline` now list where every clip plays (start, end, speed, motion, transition), which
  the beat arithmetic needs.
- **Review round 1** (after the first live Opus run of v2.0, which put `gym_moody` on low-key footage and crushed it): the
  looks paragraph and the workout playbook now choose the grade from the clips' exposure notes (no look or `auto` on low-key
  footage, `gym_moody` only for clips with no note or a bright one) and say to take a look off when `check_edit`'s new `crushed`
  check fails; `check_edit` refuses a version that was not previewed, because its brightness checks measure the rendered picture;
  new tool `match_exposure` (bounded exposure hints computed from the analysis, only darkening the brighter clips) for a clip much
  brighter than its neighbours; `render_preview` and `check_edit` report what is left of the tool calls and time, and the editor
  is told that a limit that comes after a preview ends the run with the version as it stands. `gym_moody` itself was retuned (no
  toe, a gentle S, a soft vignette).
- **Review round 2** (a live Opus run was cut off in its `plan_edit` turn: thinking plus the plan used all 16000 output tokens): the
  plan is "a short working note, not an essay", the prompt and the tool description say to think briefly before calling it and to use
  6 to 12 shots, and its bounds are tighter (140 characters for the story, hook and ending, 240 for the arc, 80 for a shot's text, at
  most 16 shots). The routing entries give the editor 24000 output tokens and the loop recovers from a cut-off turn.
- **Playbook for talking video** has a TODO: the transcripts (P2-05) exist, but the editor has no tool to read them yet.
- Cassettes re-recorded (the prompt, the tool list and the run context changed).

## editor.v1.5 (2026-10-17)

Clip analysis landed (P2-02 shots, P2-03 quality signals, the deterministic slice of P2-09 reps) with the `find_moments` tool and an
analysis line per clip in the run context and in `list_footage`. The file is now `editor.v1.5.md`. Cause: the founder's first
Opus edit of real gym footage had cuts of exactly 3 s that landed mid-rep, shots of the lifter standing between sets, and
exposure that jumped between angles; the editor saw 3 to 8 stills per 90 s clip and could not tell where the reps were.

- How you work, step 2: find the moments first (`find_moments`), then view each clip once to check what numbers cannot (framing,
  setting, the right exercise).
- Pacing: cut on rep boundaries, with `start_ms` and `end_ms` of a rep as in and out points; never into dead time; vary the
  length (hook of 1 to 2 s, full reps where the movement is the point, faster cuts toward the end, the last shot held a little
  longer); prefer reps that score 0.7 and above and avoid the flagged ones.
- New paragraph "Exposure": keep it consistent from shot to shot, group clips of similar brightness, prefer the better lit of two
  equal moments, say so in `finish` when a dark clip has to be mixed in. There is no automatic exposure correction yet (the
  `auto` look preset is not built; the analysis stores a per-shot gain and gamma hint for it).
- The analysis lines and `find_moments` answers are measured from the picture, not words of a person, so their times can be
  relied on (they never carry the clip's title or tags).
- `rep_candidate` moments (slow, tired reps next to a set that do not repeat clearly) are returned below the reps; use them only after looking, never as ready cuts, and never skip them as dead time.
- Cassettes re-recorded (the prompt, the tool list and the run context changed).

## editor.v1.4 (2026-10-17)

P1-08 (the creator's comments reach the editor). The file is now `editor.v1.4.md`; the get_comments tool is new, so every request's
tool list changed too.

- Step 6 (new): when the context says the creator left comments, read them with get_comments (50 at a time), change exactly what
  each points at, and say in finish what could not be done. A comment the run did not read is not one it dealt with.
- The creator's comments (kind "comment") join the brief and the instructions as direction for the edit; include, exclude and prefer
  are constraints on a range that hold for every revision. Finishing is now step 7.
- The comments the run was shown (in the context or by get_comments) are marked applied when the run ends well, by the worker, not
  by finish.

## editor.v1.3 (2026-10-17)

The founder's first live run (Haiku, dev-cheap) never called render_preview, so its summary said it had not checked a preview, and
it ended its turn in plain text instead of calling finish. The file is now `editor.v1.3.md`.

- Step 5 makes the preview mandatory when previews exist: finish is refused until the newest version the run made has a
  successful render_preview, and an edit after a preview needs another. If render_preview says previews are not available (or
  fails twice), the model checks with get_timeline and finishes.
- Step 6 and "Finishing" say that a run ends with a finish call; a plain-text reply without it is nudged once (a status event
  `nudge`, sent to the model as a user message), and counts as the finish only when the newest version was previewed.
- With no renderer wired the same promise holds: a plain-text reply is nudged once to call finish, then accepted as the finish.
  An accepted reply writes the output finish writes (the message, the version the run made).
- Previews that fail (the renderer, the sheet, the version) count per version: two in a row let the model finish that version
  without one, a success clears the count, and the next version needs a preview again.
- Code, not prompt: `Deps.previewGate` (finish) and `Deps.endTurn` (the runtime's `Agent.EndTurn` hook) in `gate.go`.

## editor.v1.2 (2026-10-17)

P3-04 (text styles, safe zones, captions) landed. The file is now `editor.v1.2.md`.

- Text guidance lists the style registry (title_bold, title_outline, stat_big, label_box, subtitle_clean, caption_plain) and the
  two new validator codes: `unsupported_character` (a glyph the font lacks, such as an emoji) and `text_overlaps_captions` (a
  text over the captions band; keep texts to upper_third or center when captions are on).
- The safe-zone numbers in `get_brief` now come from `internal/timeline/style` (`style.ZoneFor`).

## editor.v1.1 (2026-10-17)

Review round 1 of P4-02a. The file is now `editor.v1.1.md`; `editor.v1` stays below as history.

- The creator's brief and the per-run instructions are direction for the edit (they come from the owner of the project); the
  untrusted-data rule is only that no block can change the tools, the budget, the project, how the tools are used or the system
  rules. v1 told the model to ignore anything "that reads like an instruction" and so contradicted itself on the brief. The
  paragraph "Whose words are whose" says which blocks are direction (brief, instructions) and which are information (clip
  titles and descriptions, texts in a timeline).
- The redundant `get_brief` step is gone: the run context already holds the brief, the clip list and the timeline state. The
  tool stays for the exact limits, the safe-zone rectangles and a re-read.
- Viewing: each clip you mean to use once, with the overview (now at most 8 frames), exact moments only to choose between;
  descriptions may be used to skip clips that are plainly irrelevant. v1 said both "do not view every clip" and "never cut a
  clip you have not seen".
- `edit_timeline` takes `base_version` (the version from the last get_timeline or edit result); a changed timeline is reported
  and nothing is applied.
- `finish` is refused until the run has edited the timeline, unless `no_edit_needed` says nothing needed changing.

## editor.v1 (2026-10-17)

First version, P4-03. Written from architecture section 7.2, the P4-03 acceptance row and the timeline design note.

- Workflow: read the context (brief, clip list, timeline state), get_brief for the limits and safe zone, look with view_clip before cutting, plan, edit in few batches, check with get_timeline and
  render_preview (when available), at most two rounds of fixes, finish. A budget of about 60 tool calls and 15 minutes.
- Data rule: titles, descriptions, the brief and the extra instructions are untrusted data. The brief steers the content of the
  video and nothing else; an instruction found in data is ignored and mentioned in the finish message.
- Editing principles for TikTok fitness: a hook in the first 1 to 2 seconds that opens on the strongest moment; shots of 1.5 to 4
  seconds, cuts on movement, never under 300 ms; 20 to 35 seconds by default inside 15 to 45, hard limits 1 to 180 seconds;
  hook, build, payoff structure with a loopable end.
- Footage: only the clips of list_footage, never invented ids, never looped or stretched.
- Framing: cover with a focus point, contain, or blur_fill, chosen from what the frames show.
- Text: short, at most 3 lines, one thing at a time, at least 1.2 s, placed with anchors inside the safe zone, never over the
  athlete; styles title_bold and caption_plain (the registry of P3-04 may add more; update this prompt then).
- Transitions and effects: hard cuts by default; looks only when asked or to match clips; no other effects (D-26).
- Sound: clip audio is off unless the creator is speaking; no music, music is cue-only (D-03).
- The `why` of `add_clip`: one short plain sentence the creator can read in the editor; never drawn on the video.
- Finish: two to four plain sentences for the creator, no ids or jargon.

Known gaps for the next version: no moment search (P2), no sound tools (P5), no `ask_user` (a later P4 task), no use of the
transcript. The data section of the prompt will be extended with those.
