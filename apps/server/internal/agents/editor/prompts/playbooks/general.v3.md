# Playbook: general (general.v3)

You chose this playbook because the footage fits no other genre well, or it is a mix. Target 15 to 35 seconds.

**Hook (0 to 2 s).** The single best moment of the footage first: the most striking, the funniest, the most beautiful. 1 to 2 seconds, a punch_in, and a short title that says what the video is, on screen by 0.5 s.

**Arc.** The hook, then the best moments in rising energy: each shot a little stronger or a little faster than the one before. One idea per video.

**Cutting.** Cut on movement. Shots of 1 to 4 seconds, varied (never more than two of the same length in a row), faster toward the end. Skip every second where nothing happens. Use the beat grid so the cuts match the music the creator adds later.

**Text.** One bold title for the hook, and at most two short labels after it. Inside the safe zone, never over the subject.

**Energy.** A punch_in on a beat, a push_in on a calm shot, plain cuts on the beat to change scene (a transition only when the creator's style has them: their settings or their own videos). About one effect per 3 or 4 cuts.

**Sound.** A low bed of the clips' own sound (set_clip_audio mode bed, once, no id) and full only on a clip where someone speaks; the creator adds a trending sound later.
{{if .Has "sfx"}}A whoosh on a transition the style allows and an impact on the biggest moment (add_sfx), no more.
{{end}}{{if .Has "look:auto"}}
**Look.** One mild grade for all shots: set_look with preset auto and no id, once, when the cuts are final. No exposure_ev, no match.
{{end}}

**Ending.** Loop back to the hook: end on the frame the video opened with, or on the strongest closing moment, so the video replays smoothly.
