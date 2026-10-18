package timeline

// Music in the export (P6-06, D-34): a licensed_track or a creator_audio item is music that is mixed into the export, unlike a trend sound
// (a cue, D-03) and a sound effect (an accent). An edit with music is music-led: the renderer mixes the music and leaves the clips' ambience
// bed out (a clip in bed mode is silent under music), keeps speech (a clip in full mode) with the music ducked under it, and normalises the
// programme to the web's loudness.

// MusicItems returns the audio items that are music in the export: the licensed tracks and the creator's audio.
func MusicItems(spec *Spec) []AudioItem {
	var out []AudioItem
	for _, t := range spec.Tracks {
		tr, ok := t.(*AudioTrack)
		if !ok {
			continue
		}
		for _, it := range tr.Items {
			switch it.(type) {
			case *LicensedTrack, *CreatorAudio:
				out = append(out, it)
			}
		}
	}
	return out
}

// HasMusic says whether the spec has music in its export.
func HasMusic(spec *Spec) bool { return len(MusicItems(spec)) > 0 }
