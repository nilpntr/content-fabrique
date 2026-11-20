# Design note: the hook lab

> Status: **built** · 2026-11-20 · Extends [edit style v1](edit-style-v1.md) (the plan and `check_edit`), the speech cuts of [D-28](../05-decisions.md#d-28-content-aware-editing-across-genres) (`internal/speechcut`) and [observability v1](observability-v1.md) (eval sets). Reads, when it exists, the playbook of the creator research engine (`docs/design/creator-research-engine.md`, proposed on `plan/creator-research`). Code: `internal/hooklab`, `internal/agents/editor/hooklab.go` and `checkhook.go`, `cmd/cfctl/hooks.go`, `suites/hook-lab.yaml`.

## 1. Why

The first two seconds decide whether a short is watched. The editor already has single checks on them (`hook_length`, `hook_title`, `hook_line`, `text_over_subject`), but it makes one opening and hopes. The hook lab makes it **compare**: three distinct openings, one rubric, a ranking and a pick, all computed in Go with no model call.

## 2. What a variant is

A variant is one way to open: the **first shot** (clip, in and out point), the **text hook** on it (words and when they are on screen) and, where the clip has speech, the **opening line** (the sentence the shot starts on, read from the transcript).

## 3. The rubric (`hooklab.Score`)

Each criterion scores 0 to 1, passes at 0.7, and is "not applicable" when there is nothing to judge or the fact is missing; a total is the weighted mean of the criteria that apply.

| Criterion | Passes when | Default weight |
|---|---|---|
| `hook_in_first_2s` | the text is on and the first word is spoken within 2 s (1.0 by 500 ms, 0.7 at 2 s, 0 after) | 2 |
| `text_off_body` | the text covers neither face nor body where the renderer shows it (`subject.FitText`): clear 1, moved or shrunk in the frame 0.8, shown later or left out fails | 2 |
| `first_frame_alive` | the first second is not dead time, not near black (lit part under 0.04) and not soft (sharpness under 0.4) | 1.5 |
| `line_starts_clean` | the shot starts on the first word of a sentence, not inside a word (the rule of `hook_line`) | 1.5 |
| `hook_readable` | the text is at most 7 words and 40 characters and stays up long enough; without text, the spoken line is at most 4 s | 1 |
| `no_filler_start` | the first word spoken is not a filler (a weak opener such as "so" scores half) | 1 |

Ranking is deterministic: total, then fewer failures, then more criteria that apply, then input order. A lead under 0.05 is called a tie, and the brief decides. A criterion's detail gives times and counts only; it never quotes the transcript or the text.

## 4. Where it runs

- **Planner step (`plan_edit`).** After the plan is checked, `hooklab.Propose` makes up to three distinct variants from the footage (the best spoken line, the best moment of the picture, the clip the plan opens on), the rubric ranks them, and the answer lists them with the pick. The lines go in an `untrusted_data` block. The title is not written yet, so the lab judges the **room** for one on each first shot. The whole lab is the run's `hook_lab` artifact; the pick is `Plan.HookLab`. The step never fails a plan, and says nothing when the footage gives no choice.
- **`check_edit`: `hook_score`.** The opening that was built, with its real text, on the same rubric; it fails under 0.60 and names the lab's pick when the edit opened elsewhere and scored lower. It is in the report only when the plan ran the lab, so the scores of stored versions in the eval suites do not move.
- **`cfctl hooks lab`** runs it on a project (the current opening next to the proposals). **`cfctl hooks eval`** runs `suites/hook-lab.yaml`: made-up cases with the answer the lab must give, no database, no model, the same in `LLM_MODE=replay`.

## 5. "What works" weights

The rubric reads its weights through `hooklab.Playbook` (`HookFindings() (version, []Finding)`); `Resolve` turns them into weights. A finding names a criterion, an effect (a ratio to the creator's median) and a confidence. Only medium and high findings of the `hook` dimension count, and a finding scales a default weight by its effect, held to 0.5x to 2x: a hint, never a rule, as the research note says. Today there is no playbook (`NoPlaybook`, the defaults above); `LoadPlaybook` reads a `findings.yaml` for `cfctl hooks lab --playbook`, and `editor.Deps.HookPlaybook` is where the research engine plugs in.

## 6. Not built, on purpose

- **The model does not write the three text hooks yet.** That needs a `hook_lab` tool in the editor's tool contract, whose Go copy is generated, and a prompt version; this slice proposes shots and lines in Go and scores the text once it exists. The rubric and `hooklab.Check` (three distinct variants) are ready for it.
- **No per-frame blur.** Sharpness comes from the moments of the clip analysis (rep, hold, shot); a clip without them is not judged on it.
- **The findings file's shape is ours** until the research engine fixes it: `criterion` is the one key it must add to a hook finding.
