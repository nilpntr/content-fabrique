# Playbook: talking to camera and opinion (talking.v2)

You chose this playbook because a person speaks to the camera: an opinion, a take, a story, advice. Target 20 to 45 seconds.

**Hook (0 to 2 s).** The boldest line, often lifted from the middle of the take and placed first. Open on it with a punch_in and a short title that states the claim in 3 to 6 words, on screen by 0.5 s.

**Cutting.**
- Jump cuts: remove filler words, long pauses, restarts and breaths, so the speaker never waits. Keep the sentences whole, and cut on a pause between words rather than in the middle of a word.
- Shots are one or two sentences long, 2 to 6 seconds, and they vary. A jump cut that lands on the same framing looks like a mistake: change the framing a little between neighbouring shots with a punch_in on an emphasis word (strength 0.1 to 0.15).
- Cover a jump with text or a cutaway when the footage has one.
- The transcript is not available to you yet, so judge the speech by looking and by the clip's description. When you cannot tell where a sentence ends, prefer fewer, longer cuts to cuts in the middle of words, and say so in finish.
- TODO (transcripts exist in the `transcripts` table, with sentences, filler words and pauses, but the editor has no tool to read them yet): when it has one, cut the filler words and the pauses the transcript lists, and start and end each shot on a sentence boundary.

**Framing.** The speaker stays centred in 9:16: use cover with the focus on the face (x about 0.5, y about 0.35). A landscape clip gets blur_fill only when cropping would cut the speaker off.

**Text.** Captions are the main text: word-highlight captions on (set_captions), and then texts only in the upper third. One keyword on screen at a time when a point needs emphasis.

**Energy.** Restraint: a punch_in on the emphasis, now and then a short zoom_through for a change of topic. No whips, no ramps.

**Sound.** The speaker's audio is the point: set_clip_audio mode full on every clip with speech (it is the programme, ducked under any sound effect). A clip without speech, such as a cutaway, is a bed.

**Ending.** The last line lands, then a short beat of silence or a call to action in text. A clean end that can loop back to the hook.
