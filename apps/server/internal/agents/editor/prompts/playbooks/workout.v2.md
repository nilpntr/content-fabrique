# Playbook: workout and fitness (workout.v2)

You chose this playbook because the footage is training: lifts, sets, a pose, a transformation. Target 15 to 35 seconds.

**Hook (0 to 2 s).** The most striking frame first: a peak flex, the heaviest lockout, the best rep. The first shot is 1 to 2 seconds long, it gets a punch_in that lands on the first beat, and a big title (title_bold or title_outline, 2 to 4 words) is on screen by 0.5 s. Nothing slow happens before 1.5 s.

**Rhythm.**
- Every cut sits on the beat grid (the tempo in your plan; 120 to 128 BPM when no sound is chosen).
- Vary the length: 1 to 2 beats for hype moments, and a full rep only where it is worth watching. A rep at 124 BPM is about 7 beats, so to fit a shot into 2 to 4 beats take the strongest half of the rep (from one turning point to the next), or play the rep faster with set_speed 2 or a speed ramp. Never put more than two shots of the same length in a row.
- The cuts speed up toward the end. The last shot is held a little longer.

**Movement and continuity.**
- Cut at the top or the bottom of the movement, never in the middle: a shot starts and ends on a rep's start_ms, its end_ms or its turning point half-way between (check_edit accepts those, within 150 ms).
- Cut on motion where you can (the bar going up in one shot, the next shot starting on the same movement). Skip dead time completely.
- Prefer the cleanest, sharpest reps (score 0.7 and above). Keep the exposure even from shot to shot: the footage is often low-key (a lit lifter in a dark gym), which is fine, so do not try to brighten it, but do not alternate a very dark angle with a much lighter one.

**Energy.** A punch_in on a beat hit (at_ms on the beat, inside the shot), one speed ramp on the best rep at the most (fast into slow at the peak contraction), a whip_left or whip_right or a zoom_through between two exercises, and a flash on the drop. About one effect per 3 or 4 cuts, no more.

**Text.** One bold title for the hook. A short uppercase label (label_box) when a new exercise starts, 1 to 2 seconds, inside the safe zone and never over the lifter. Word-highlight captions only when there is speech and the brief wants them.

**Sound.** The clips' own sound is the gym: set_clip_audio mode bed once with no id, so there is a low ambience under every shot (full only for a clip where someone coaches or talks). The creator adds a trending sound in TikTok over the export; the beat grid you write is so the cuts will match it.
{{if .Has "sfx"}}Add generated sound effects with add_sfx on top of the bed, with restraint: a whoosh at the start of each whip or zoom_through, a sub_drop together with an impact on the drop (the flash), an impact on the hook's punch_in, a riser (tension) from the first frame when the hook's first cut is about 1.5 s in, so it peaks on that cut, and a sting on the last frame when the video loops. At most one effect sound per visual effect, and variant "auto" so repeated whooshes differ.
{{end}}{{if .Has "look:gym_moody"}}
**Look.** One grade for every shot, never a different one per shot, set once with set_look and no id after the cuts are final, and chosen from the clips' exposure notes. Gym footage is usually low-key (a lit lifter in a dark room), and a grade that darkens it crushes it to near black. So when the clips are noted low-key or underexposed, use no grade at all, or at most auto (a gentle curve that leaves the shadows alone). Use gym_moody (warm skin, a little contrast, a soft vignette; it leaves the shadows alone too) when the clips have no exposure note or are brighter. After a look, render_preview and check_edit: its crushed check measures the render against the footage, and if it fails, take the look off (set_look none) and say so in finish. Do not set exposure_ev or match yourself. When one clip is much brighter than the gym clips (a posing clip among them), call match_exposure: it sets bounded hints from the analysis that darken the brighter clip toward the rest.
{{end}}
**Ending.** Loop back to the hook: end on a held pose or the same frame the video opened on, so the video replays smoothly.
