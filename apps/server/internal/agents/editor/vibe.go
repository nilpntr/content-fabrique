package editor

import "fmt"

// The vibe of an edit (P6-06): the energy, chosen by the creator per edit (calm, lively or hype; lively by default). The creator's
// style profile decides the look (colours, fonts, the title's look and place) and the rough shot length; the vibe decides how much moves:
// the pace of the cuts on the beat, text that enters and leaves, a camera that never sleeps. No vibe asks for transitions: plain cuts on
// the beat are the default, because creators read effect transitions as an automatic AI edit (the creator's settings or their own
// videos may still ask for them). A profile that measured "no effects" does not switch text animation or the camera off.

// The vibes.
const (
	VibeCalm    = "calm"
	VibeLively  = "lively"
	VibeHype    = "hype"
	DefaultVibe = VibeLively
)

// Vibes lists the vibes, calm to hype.
var Vibes = []string{VibeCalm, VibeLively, VibeHype}

// ValidVibe says whether v is a vibe ("" is not: use VibeOr).
func ValidVibe(v string) bool { return v == VibeCalm || v == VibeLively || v == VibeHype }

// VibeOr is v, or the default when v is empty or unknown.
func VibeOr(v string) string {
	if ValidVibe(v) {
		return v
	}
	return DefaultVibe
}

// energetic says whether a vibe asks for transitions, text entrances and a moving camera whatever the creator's effects verdict.
func energetic(v string) bool { return VibeOr(v) != VibeCalm }

// vibeNote is the paragraph the editor reads about the vibe of this run, at the start of the run (the opening message, so the cached
// prefix is the same for every run).
func vibeNote(v string) string {
	const cuts = "Plain cuts on the beat, no effect transitions, unless the creator's settings below ask for them: creators read punch, zoom and whip transitions as an automatic AI edit."
	switch VibeOr(v) {
	case VibeCalm:
		return "Vibe of this edit: calm. Keep close to the creator's own restraint: cuts on the strong beats, shots that breathe, the title fades in and out, and the camera still breathes (handheld on every static shot longer than 2 s). " + cuts
	case VibeHype:
		return "Vibe of this edit: hype. Maximum energy within the creator's look: shots of 1 to 2 beats after the hook, every cut on a beat or a hit, a punch-in (set_motion handheld at_ms) on the drop and the strong beats, every text enters with pop or slide_up and leaves with an exit, and every shot moves (handheld). " + cuts
	}
	return fmt.Sprintf("Vibe of this edit: %s (the default). The creator's style decides the look and the rough shot length; the vibe decides the energy: the hook ends on the drop, the cuts land on the beats and the phrase starts, a punch-in on the drop, every text enters (pop, slide_up or fade) and leaves (an exit), and every static shot gets the handheld living camera. The creator's 'no effects' means no gimmicks, not no motion. %s", VibeLively, cuts)
}
