# Playbook: vlog and lifestyle (vlog.v4)

You chose this playbook because the footage is a day, a trip, a routine or a place: moments in time rather than one subject. Target 20 to 45 seconds.

**The format.** The format you chose in plan_edit (from list_formats) sets the structure: its hook, its middle, its payoff and its text pattern, timed against the song's drop. This playbook is the craft of the genre inside that structure. Where they differ on structure (how long the hook runs, where the payoff lands, which text the video has), the format wins; on craft (the cuts, the rhythm, the movement, the sound), this playbook does. The creator's style and settings still decide the look.

**Hook (0 to 2 s).** The most beautiful or the most surprising moment of the whole footage, shown first, with a short title that sets the scene ("A DAY IN LISBON") on screen by 0.5 s.

**Arc.** Follow the time of day, or a before and after. Move quickly through transit and filler (set_speed 2 to 4, or cut it), and slow down for the moments with emotion: a view, a laugh, a meal, a goal reached. Cuts are quick in busy stretches (1 to 2 beats) and longer on a feeling (4 to 8 beats). Never more than two shots of the same length in a row.

**Cutting.** Cut on a movement (a turn of the head, a step, a pan), keep the best second of each clip and skip the rest, and let the order tell the story of the day. Cover a gap in time with a short crossfade, never a long one.

**Text.** Short place and time labels (label_box or subtitle_clean, 1 to 2 seconds): "MORNING", "LISBON". At most one text on screen at a time, in the upper third.

**Energy.** Light: a punch_in on a beat now and then, a plain cut on the beat to move to a new place (a transition only when the creator's style has them: their settings or their own videos). One effect per 3 or 4 cuts.

**Sound.** Keep the natural sound of a clip when it carries the place (waves, a street, a laugh) and the clip has sound: a bed (set_clip_audio mode bed) for the ambience, full for someone talking. The pacing follows the beat grid, and the creator adds music in TikTok.

{{if .Has "look:auto"}}**Look.** One mild grade for all shots: set_look with preset auto and no id, once, when the cuts are final. No exposure_ev, no match.
{{end}}
**Ending.** Come back to where it began: the same view at the end of the day, or the last smile, so the video can loop.
