# Design note: formats v1, teaching the editor ideas

> Status: **built** · 2026-10-18 · [D-40](../05-decisions.md#d-40-a-format-library-the-editor-picks-the-idea-first). Extends [style profile v1](style-profile-v1.md) (how a creator edits), [edit style v1](edit-style-v1.md) (the genre playbooks) and [observability v1](observability-v1.md) (the eval suite). Code: `config/formats/*.yaml`, `internal/formats`, `internal/styleprofile/formats.go`, `internal/agents/editor/formats.go` and `checkformat.go`, `internal/agents/runapi` (`new_idea`), the web's storyboard and "Try another idea".

## 1. Why

The founder: "learn how you can also teach the model to be creative, so it also comes up with new ideas". The style profile teaches the editor how one creator edits; it gives no ideas. Short-form video works in a small set of formats (a "<TOPIC> 101" tutorial, a numbered list, a quote then the reveal on the drop, a transformation, a dialogue then the twist, a story told in captions, ...). A library of those formats lets the editor pick a concept that fits the footage before it cuts, mix a device in, and propose one the creator has not tried; "Try another idea" gives a creator a differently-concepted version on demand.

## 2. The library (`config/formats`, package `formats`)

One YAML card per format, embedded in the binary (`configfiles.Formats()`), in our own words (never a creator's title or line), loaded strictly (an unknown field fails) and validated (`Card.Validate`, tested on every card): 12 formats and one device (`loop_ending`, mixed into a format, never tagged on a video). A card has:

| Field | What it holds |
|---|---|
| `id`, `version`, `kind`, `name`, `plural`, `pitch` | the format; `name` is what the creator reads ("Transformation reveal"), `plural` the list form ("transformation reveals") |
| `fits` | `content_types`, the footage `needs` (all), `needs_any`, `helps` (signals, below), `when` in words |
| `not_when` | when it does not fit |
| `beats` | `hook`, `middle`, `payoff` in order: the plan roles that play each, the timing against the song's drop, what it shows |
| `text` | the pattern (`single_title`, `story_captions`, `dialogue`, `chapter_cards`, `none`, `any`), title forms with slots (`<TOPIC> 101`, `<N> <TOPIC> FOR <GOAL>`, `POV: <SITUATION>`), notes |
| `music`, `length_s`, `pace`, `pace_hint`, `vibes` | how it uses the song, its usual length and pace, how it plays at calm, lively and hype |
| `how_it_plays` | one to three anonymised descriptions |
| `detect` | `keywords` and `rules` over the cues (section 3) |
| `check` | what `format_followed` holds an edit to: where the payoff lands (`drop`, `end`, `any`), a `loop`, `min_shots` |

The library's version is `formats@<hash of the files>`: a profile tagged with an older library is tagged again when it is read. **Adding a format** (from the `viral_*` folders to come) is a new card: rules combine the existing cues and the card's own keywords, so most new formats need no code; the plan's enum in the tool contract (`packages/schemas/agents/editor-tools.v1.json`) gets the id (a test fails until it does).

## 3. Detection on the creator's references (no model call)

`styleprofile.FormatReference` turns a reference's measures (the cuts and shots, the drop and the lift, the camera) and the text the style read already reads (the title, the chapter titles, a line per shot, the pattern) into a `formats.Reference`; `Library.Detect` computes the cues and scores every card's rules (the best rule of a card is its score, 0.5 at least to be a tag). The cues: the four text patterns; `title_series` (a title ending in a number such as 101); `title_numbered` (a count title); `chapters` (two numbered chapter titles); `hook_quote` (the hook's text in quotes, or a first-person thought of five words or more); `cut_at_drop`; `few_cuts`, `many_cuts`, `montage`, `long_take`; `handheld`; `first_person` (in English, Dutch and German); `versus` (two values set against each other, also over two shots: "92kg vs" on one, "78kg" on the next); `numbers_rise`; and `keyword` / `keyword_hook` for the card's words. The text is the creator's and untrusted: it is only matched, never stored again or shown.

`Profile.Formats` keeps the share of each format (by the most likely tag of each reference). "Your style" shows it as a measure line ("Your go-to formats: 101 tutorials (4 of 10 videos), one-take sets (3 of 10), ..."), the editor's style block names the ids, and `cfctl style analyze` prints the tags of every file. `cfctl style analyze --measures-out` keeps the measures of a local run (they hold the creator's words: never committed) and `cfctl style formats --measures` tags them again with no model and no database, to try a change to the library.

## 4. The editor

- **`list_formats`** ranks the cards for this project: the footage signals (`reps`, `two_exercises`, `three_exercises`, `big_set`, `pose`, `then_clip`, `talking`, `product`, `lifestyle`, `clips_2`, `clips_3`, `many_clips`, `long_clip`, `song`) from the clips' analysis and words and the brief; the fit (needs met, helps, the likely content type, a vibe the format plays weak at); the creator's share; two fitting formats the creator never used marked NEW TO THE CREATOR; for a new idea the formats of the earlier versions marked not for this edit; with `all` what does not fit and what it would need.
- **`plan_edit`** takes `format` (an enum of the library's formats), `format_why` (a sentence) and optional `format_mix`. The plan records `format: {id, name, why, mix, mix_name, new_to_creator, new_idea, avoided, library}`; the answer adds the card's beats and notes (no shot for the payoff, too few shots, a length outside the format's, two new formats at once, a format whose needs list_formats did not see: a note, not a refusal, because the words can miss what a clip holds). A new idea's plan may not reuse an avoided format.
- **Prompt `editor.v2.11`**, "Pick a format first": fit first, the creator's formats rank higher, at most one new format per edit unless the creator asked for something new, a new idea is a new concept with the same style, the format sets the structure and the creator's style the look; the creator's own text-pattern setting wins over the format's. All six playbooks say the format sets the structure and the playbook the craft (new versions).
- **`check_edit`'s `format_followed`**: the hook carries the format's text (a title by 1 s, a count title and two numbered chapter cards, a first caption and a line on most shots, two dialogue lines and a bare twist, or no text), the payoff lands on the song's drop for a reveal (or the plan has a reveal), a loop ends on the opening clip, the edit has the shots the format needs; a hook title outside the format's forms is "borderline", advice only. The text checks (`story_captions` and the others) follow the format's pattern over a measured or suggested one, never over the creator's own setting. The check is creator-agnostic: no creator's names, titles or lines in code or prompts.

## 5. "Try another idea"

`POST /v1/projects/{id}/edit` with `new_idea: true` (the API reads the formats of the project's versions that runs made, newest first, at most four, from their plans) starts a run whose input says so (`new_idea`, `avoid_formats`); the opening tells the editor to make a differently-concepted version, not a revision. It is priced like any other edit of the project (a revision once one was charged) and needs a version (`409 no_video_yet`). `GET /v1/runs/{id}` has `new_idea`. In the app a "Try another idea" button sits next to "Make the changes" (it sends the typed words too), its line says it costs the same as a change; the version a new idea made is named "Version 3 · new idea: transformation reveal", the progress card says "I'm trying another idea", and the storyboard opens with "The idea: Transformation reveal", why it fits and "New for you".

## 6. Evals

`judge_edit.v5` (the calibration anchors of `judge_edit.v4` plus the formats) is shown the chosen card (name, pitch, beats; a new idea and "new to this creator" said) and the creator's usual formats (in the style profile), and is told how to score concept and novelty items. Suites get a suite-wide `rubric` (every case but the calibration ones; a case's item of the same id wins): `clear-concept`. A case with `new_idea: true` is judged on its stored version, and with `--live` is made as a new idea on its stored project, avoiding the formats of the versions up to `idea_from` (default: the stored version), so every `--repeat` sample avoids the same formats; `back-day-new-idea` has a `novelty` item. The baseline cache hashes the format library (`internal/formats`, `config/formats`) and `cfctl`'s `eval_suite.go` (how a live case is made) besides #165's paths; the suite file (its rubric and cases) is hashed already.

## 7. Checking the library on real references

Run `cfctl style analyze --no-store --workspace <wks> --measures-out /tmp/x.json --files ...` (one metered Haiku text read per reference, about $0.001) and then `cfctl style formats --measures /tmp/x.json` after each change to the cards. The outputs hold a creator's words and are never committed.

## 8. Not built

- A format proposed to the creator before the edit (the plan is shown, not asked); several ideas side by side.
- Detection from the picture (a then/now split, a counter graphic) or from the audio; detection of the loop ending.
- Learning new cards from viral examples automatically: a person writes the card from the `viral_*` folders, the tagging run checks it.
