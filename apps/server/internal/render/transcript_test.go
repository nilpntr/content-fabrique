package render

import "hash/fnv"

// fixtureTranscript is the transcript the tests give an asset: a word every 420 ms, each 360 ms long, from a short fitness
// vocabulary that starts at a different place for every asset (so a caption taken from the wrong asset reads differently), with
// a full stop after every seventh word. P2-05 produces real transcripts; the renderer only takes them through the resolver.
func fixtureTranscript(assetID string, durationMs int64) []Word {
	vocab := []string{"never", "skip", "leg", "day", "squat", "deep", "and", "stay", "tight", "drive", "through", "your", "heels", "every", "single", "rep"}
	h := fnv.New32a()
	_, _ = h.Write([]byte(assetID))
	off := int(h.Sum32() % uint32(len(vocab)))
	var out []Word
	for i := 0; int64(i)*420+360 <= durationMs; i++ {
		w := vocab[(off+i)%len(vocab)]
		if i%7 == 6 {
			w += "."
		}
		out = append(out, Word{Text: w, StartMs: int64(i) * 420, EndMs: int64(i)*420 + 360})
	}
	return out
}
