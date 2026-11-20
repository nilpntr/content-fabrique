# Playbook: talking to camera and opinion (talking.v5)

You chose this playbook because a person speaks to the camera: an opinion, a take, a story, advice. Target 20 to 45 seconds.

**The format.** The format you chose in plan_edit (from list_formats) sets the structure: its hook, its middle, its payoff and its text pattern, timed against the song's drop. This playbook is the craft of the genre inside that structure. Where they differ on structure (how long the hook runs, where the payoff lands, which text the video has), the format wins; on craft (the cuts, the rhythm, the movement, the sound), this playbook does. The creator's style and settings still decide the look.

**Hook (0 to 2 s).** The boldest line, often lifted from the middle of the take and placed first. Open on it with a punch_in and a short title that states the claim in 3 to 6 words, on screen by 0.5 s. Read the sentences with get_transcript, pick the boldest whole sentence and start the first shot on its first word (hook_line checks it); a sentence that runs longer than the hook may take the first shot to its natural end rather than be cut in half.

**Cutting.**
- Call get_transcript for each clip with the person talking before you cut it. It lists the sentences with their times and proposes keep ranges: the fillers (um, uh, you know, ...) and every pause over 400 ms are left out, and every cut falls in a gap between two words with 80 ms of padding. Use the keep ranges as the in and out points of your shots (the times are the clip's own, like the source of add_clip). speech_cut_clean checks it: no cut inside a word, no filler kept, no pause over 400 ms.
- Keep the sentences whole. A shot is one or two sentences, 2 to 6 seconds, and they vary; join neighbouring keep ranges of the same sentence in one shot instead of cutting inside it. Remove a restart or a breath that the proposal kept, only in a gap between words.
- A jump cut that lands on the same framing looks like a mistake: change the framing a little between neighbouring shots with a punch_in on an emphasis word (strength 0.1 to 0.15).
- Cover a jump with text or a cutaway when the footage has one.
- A clip with no transcript yet (get_transcript says so) or no speech is cut on what you see: prefer fewer, longer cuts to cuts in the middle of words, and say so in finish.
- Where get_transcript says a cut was left in because the time of a word is not certain, leave it: check_edit does not hold it against you.

**Framing.** The speaker stays centred in 9:16: use cover with the focus on the face (x about 0.5, y about 0.35). A landscape clip gets blur_fill only when cropping would cut the speaker off.

**Text.** Captions are the main text: word-highlight captions on (set_captions), and then texts only in the upper third. One keyword on screen at a time when a point needs emphasis.

**Energy.** Restraint: a punch_in on the emphasis, a plain cut for a change of topic (a transition only when the creator's style has them: their settings or their own videos). No ramps.

**Sound.** The speaker's audio is the point: set_clip_audio mode full on every clip with speech (it is the programme, ducked under any sound effect). A clip without speech, such as a cutaway, is a bed.

**Ending.** The last line lands, then a short beat of silence or a call to action in text. A clean end that can loop back to the hook.
