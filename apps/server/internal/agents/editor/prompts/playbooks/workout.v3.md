# Playbook: workout and fitness (workout.v3)

You chose this playbook because the footage is training: lifts, sets, a pose, a transformation. Target 15 to 35 seconds.

**Hook (0 to the drop).** The most striking frame first: a peak flex, the heaviest lockout, the best rep, the back pose. With a song the hook runs until its drop (2 to 4.5 s in) and the first cut lands on the drop as a punch, zoom_through or whip: the reveal on the hit. Without one the first shot is 1 to 2 seconds long. A big title (in the creator's style when there is one; else title_bold or title_outline, 2 to 4 words) pops in by 0.5 s, in the top band above the head, and leaves before the drop. Nothing sits still before the drop: the hook shot has the living camera or a punch_in on the first beat.

**Rhythm.**
- Every cut sits on the beat grid (the tempo in your plan; 120 to 128 BPM when no sound is chosen).
- Vary the length: 1 to 2 beats for hype moments, and a full rep only where it is worth watching. A rep at 124 BPM is about 7 beats, so to fit a shot into 2 to 4 beats take the strongest half of the rep (from one turning point to the next), or play the rep faster with set_speed 2 or a speed ramp. Never put more than two shots of the same length in a row.
- The cuts speed up toward the end. The last shot is held a little longer.

**Movement and continuity.**
- Cut at the top or the bottom of the movement, never in the middle: a shot starts and ends on a rep's start_ms, its end_ms or its turning point half-way between (check_edit accepts those, within 150 ms).
- Cut on motion where you can (the bar going up in one shot, the next shot starting on the same movement). Skip dead time completely.
- Prefer the cleanest, sharpest reps (score 0.7 and above). Keep the exposure even from shot to shot: the footage is often low-key (a lit lifter in a dark gym), which is fine, so do not try to brighten it, but do not alternate a very dark angle with a much lighter one.

**Angles.** A set is not one 10 second tripod take. With two angles of an exercise (front and behind, close and wide), switch between them every 2 to 5 s at rep boundaries, on a beat; with one angle, keep it running with the living camera and a punch-in on a strong beat.

**Energy (by the vibe).** Every shot has the living camera (set_motion handheld with no id), and the shot that holds the drop or a strong beat a handheld punch-in at_ms there. At lively: the drop is a punch or zoom_through, a whip_left or whip_right between two exercises, a flash on the biggest moment, hard cuts on ordinary beats; 250 to 400 ms each and never the same one three times in a row. At hype: transitions on every phrase start and strong beat, and shots of 1 to 2 beats after the hook. At calm: hard cuts and one transition on the drop. A speed ramp on the best rep at the most, and none for a creator of no effects.

**Text.** One title for the hook, popping in and leaving with an exit. A video of two or more exercises is a listicle: a numbered chapter title per exercise ("1. ROWS FOR THICKNESS", in the creator's title style and language) slides up with the exercise's first shot, in the top band above the head, and fades before the next. Never over the lifter's body. Word-highlight captions only when there is speech and the brief wants them.

**Sound.** With a song, the export is the song alone (no gym clatter under it): set_sound kind creator_audio with source_offset_ms so its drop lands at the end of the hook, and cut on its beats. Without one, the clips' own sound is the gym: set_clip_audio mode bed once with no id, so there is a low ambience under every shot (full only for a clip where someone coaches or talks), and the creator adds a trending sound in TikTok over the export; the beat grid you write is so the cuts will match it.
{{if .Has "sfx"}}Add generated sound effects with add_sfx on top of the bed, with restraint: a whoosh at the start of each whip or zoom_through, a sub_drop together with an impact on the drop (the flash), an impact on the hook's punch_in, a riser (tension) from the first frame when the hook's first cut is about 1.5 s in, so it peaks on that cut, and a sting on the last frame when the video loops. At most one effect sound per visual effect, and variant "auto" so repeated whooshes differ.
{{end}}{{if .Has "look:gym_moody"}}
**Look.** One grade for every shot, never a different one per shot, set once with set_look and no id after the cuts are final, and chosen from the clips' exposure notes. Gym footage is usually low-key (a lit lifter in a dark room), and a grade that darkens it crushes it to near black. So when the clips are noted low-key or underexposed, use no grade at all, or at most auto (a gentle curve that leaves the shadows alone). Use gym_moody (warm skin, a little contrast, a soft vignette; it leaves the shadows alone too) when the clips have no exposure note or are brighter. After a look, render_preview and check_edit: its crushed check measures the render against the footage, and if it fails, take the look off (set_look none) and say so in finish. Do not set exposure_ev or match yourself. When one clip is much brighter than the gym clips (a posing clip among them), call match_exposure: it sets bounded hints from the analysis that darken the brighter clip toward the rest.
{{end}}
**Ending.** Loop back to the hook: end on a held pose or the same frame the video opened on, so the video replays smoothly.
