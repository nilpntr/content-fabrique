package editor

import "fmt"

// The vibe of an edit (P6-06): the energy, chosen by the creator per edit (calm, lively or hype; lively by default). The creator's
// style profile decides the look (colours, fonts, the title's look and place) and the rough shot length; the vibe decides how much moves:
// transitions on the strong beats and the drop, text that enters and leaves, a camera that never sleeps. A profile that measured "no
// effects" no longer switches transitions and text animation off at lively and hype; calm stays close to the creator's restraint.

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
	switch VibeOr(v) {
	case VibeCalm:
		return "Vibe of this edit: calm. Keep close to the creator's own restraint: hard cuts on the beat, at most one transition (on the drop), the title fades in and out, and the camera still breathes (handheld on every static shot longer than 2 s)."
	case VibeHype:
		return "Vibe of this edit: hype. Maximum energy within the creator's look: shots of 1 to 2 beats after the hook, a transition (whip_left, whip_right, zoom_through, punch or flash, 250 to 400 ms, never the same one three times in a row) on every strong beat, phrase start and the drop, a punch-in on the drop, every text enters with pop or slide_up and leaves with an exit, and every shot moves (handheld)."
	}
	return fmt.Sprintf("Vibe of this edit: %s (the default). The creator's style decides the look and the rough shot length; the vibe decides the energy: the hook ends on the drop with a transition, cuts on strong beats and phrase starts become whip_left, whip_right, zoom_through, punch or flash (250 to 400 ms; never the same one three times in a row) while cuts on ordinary beats stay hard, every text enters (pop, slide_up or fade) and leaves (an exit), and every static shot gets the handheld living camera. The creator's 'no effects' means no gimmicks, not no motion.", VibeLively)
}
