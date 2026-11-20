# Proof edits and clip feedback (v1)

> Status: built (2026-11-20), [D-42](../05-decisions.md#d-42-proof-edits-and-feedback-stored-by-clip-and-second). Source: rule 15
> of a creator's edit rules (`docs/design/gino-edit-rules.md` on `plan/gino-rules`, from his own skill, step 5).

## The rule

Every new edit and every revision is first shown as a **proof**: a small number at the top right of each shot (1, 2, 3 … in the
order they play) and a shot list (number → clip + second). The creator answers by number: "never use shot 5 again", "shot 6 a few
seconds later". That feedback is stored against the **clip and the second**, never the number (the numbers change with the next
version). When the creator says so, **make it final**: the same edit, rendered without numbers.

It is the creator's choice: the style setting `proof_edits` (`off` by default; only the creator's own setting turns it on, no
suggestion or measure does).

## Pieces

| Piece | Where |
|---|---|
| Shot numbering | `timeline.Shots`: the playable main-track clips sorted by `at_ms` (stable), numbered from 1. A shot runs from its start to where the next one starts. The renderer lays the clips out in the same order, so the burned number and the list agree. |
| The render | `render.Proof` = the preview profile with `ShotNumbers`. Each number is an ASS event (style `shot_number`: Inter Bold, em ≈ 28 px at 1080, white with a thin black outline, the drawtext look of the creator's own proofs) at the top right of the safe zone, under the PREVIEW watermark, from the shot's first frame to the next shot's first. A number that cannot fit the safe zone is left out with a `shot_number_dropped` warning. |
| Renders | `renders.proof` (migration 00029): only a preview can be a proof (check constraint); the dedupe key is (project, version, profile, proof), so a version can have a plain preview and a proof. `POST /v1/projects/{id}/renders` takes `proof`; left out, a preview follows the creator's `proof_edits`. The editor's own `render_preview` follows it too. What measures or judges the picture (`check_edit`'s rendered checks, the eval judge) takes a plain preview of the version before a proof (`store.PlainFirst`). The web shows only the kind the creator chose. |
| Make it final | `POST /v1/renders/{id}/final`: the final render of that render's version, without numbers, by the same rules (credits, cap) as a final render. The web's "Make it final" button calls it on the proof on screen. |
| Clip feedback | `clip_feedback` (migration 00029), package `clipfeedback`. A row is (clip, kind, `[start_ms, end_ms)`) plus where it was given (project, version, shot number, item) and an optional note (untrusted text, ≤ 500 characters). `never_use`: the shot's source range, or the whole clip. `start_later` (0.5 to 30 s): the range from the shot's in point to the new start. The same thing about the same range again is one row. |
| API | `GET /v1/projects/{id}/timelines/{version}/shots`, `POST/GET /v1/clip-feedback`, `DELETE /v1/clip-feedback/{id}` (owners and editors write; every role reads; another workspace's ids are 404). |
| The editor | `get_clip_feedback` lists the rules on the project's clips (notes in a data block); the run context says when there are some; `check_edit`'s `clip_feedback` check (only in the report when an edit's clips have feedback) fails a shot that plays more than 100 ms of a ruled-out range, with the shot, the clip, the seconds and what to change. Prompt `editor.v2.14`. |
| cfctl | `cfctl proof shots | never | later | list | forget`, `cfctl render --proof` (a local proof and its shot list), `cfctl style set --proof-edits on`. |
| Web | With `proof_edits` on: the "Shots" card next to the video (number, clip, second; "Never use again", "Start later…" 1/2/3/5 s, Undo) and "Make it final" instead of "Download". The two kinds of feedback differ by icon and words, in orange and blue, never red and green. "Shot numbers first" in Your style. |

## Choices

- **A proof is a preview**, half size from the proxies. A full-quality proof would cost a final render for each round of feedback;
  the numbers are what matters, and "make it final" gives the full quality.
- **Feedback holds across projects** of the workspace (a workspace is one creator), for the clip. A clip deleted from the library takes
  its feedback with it.
- **The 100 ms tolerance** absorbs frame snapping and the snap of a cut to the beat; it is not the creator's shot coming back.
- **find_moments** leaves out moments inside a ruled-out range and says how many. **Not yet**: the shot list is not in the
  run feed; feedback cannot be given on the final render.
