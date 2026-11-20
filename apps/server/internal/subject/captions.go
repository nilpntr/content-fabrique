package subject

import (
	"github.com/nilpntr/content-fabrique/internal/timeline"
	"github.com/nilpntr/content-fabrique/internal/timeline/style"
)

// Placing the captions (Captions v2): a clip's captions sit in the lower band by default, or in the upper one when the speaker's face is
// in the lower. The choice is made per clip, from the subject boxes the text placement already uses (Regions: the quick look's boxes
// mapped through the clip's reframe and camera), so the captions do not jump between the two bands inside a shot. Only the face is kept
// clear (the head and shoulders, D-38); a caption may stand on the chest. The renderer and the caption_over_face check both call
// CaptionPlaces, so the check says what the render does.

// CaptionPlace is where the captions of one main-track clip are drawn.
type CaptionPlace struct {
	// ClipID is the clip, and AtMs and EndMs the part of the output it covers.
	ClipID      string
	AtMs, EndMs int64
	// Pos is style.CaptionLower or style.CaptionUpper.
	Pos string
	// Band is the caption band at Pos.
	Band style.Rect
	// OverFace says the band at Pos still overlaps a face box: both bands do, so the captions cannot be kept clear by moving them.
	OverFace bool
	// Face is the first face box the band overlaps, for the message.
	Face style.Rect
	// Moved says the captions are in the upper band because the lower one is on the face.
	Moved bool
}

// CaptionPlaces returns the place of the captions for every clip of the main track, in time order. Without subject boxes (lookup nil, or a
// clip with none) every clip's captions are in the lower band.
func CaptionPlaces(spec *timeline.Spec, lookup Lookup, st style.Style) []CaptionPlace {
	main, _, ok := timeline.MainTrack(spec)
	if !ok {
		return nil
	}
	out := spec.Output
	zone, ok := out.Zone()
	if !ok {
		return nil
	}
	lower := style.CaptionBandAt(zone, out.Width, out.ShortSide(), st, style.CaptionLower)
	upper := style.CaptionBandAt(zone, out.Width, out.ShortSide(), st, style.CaptionUpper)
	places := make([]CaptionPlace, 0, len(main.Items))
	for _, it := range main.Items {
		end := it.AtMs + timeline.OutputDuration(it)
		p := CaptionPlace{ClipID: it.ID, AtMs: it.AtMs, EndMs: end, Pos: style.CaptionLower, Band: lower}
		var faces []style.Rect
		if lookup != nil {
			for _, s := range Regions(spec, lookup, it.AtMs, end) {
				faces = append(faces, s.Head)
			}
		}
		lowerHit, lowerArea := firstOverlap(lower, faces)
		if lowerArea == 0 {
			places = append(places, p)
			continue
		}
		upperHit, upperArea := firstOverlap(upper, faces)
		switch {
		case upperArea == 0:
			p.Pos, p.Band, p.Moved = style.CaptionUpper, upper, true
		case upperArea < lowerArea:
			p.Pos, p.Band, p.Moved, p.OverFace, p.Face = style.CaptionUpper, upper, true, true, upperHit
		default:
			p.OverFace, p.Face = true, lowerHit
		}
		places = append(places, p)
	}
	return places
}

// firstOverlap is the first face the band overlaps and the largest overlapping area in pixels (0 when the band is clear of every face).
func firstOverlap(band style.Rect, faces []style.Rect) (style.Rect, int) {
	var first style.Rect
	most := 0
	for _, f := range faces {
		if !band.Overlaps(f) {
			continue
		}
		w := min(band.Right, f.Right) - max(band.Left, f.Left)
		h := min(band.Bottom, f.Bottom) - max(band.Top, f.Top)
		if a := w * h; a > 0 {
			if most == 0 {
				first = f
			}
			most = max(most, a)
		}
	}
	return first, most
}
