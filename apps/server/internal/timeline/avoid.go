package timeline

import (
	"math"

	"github.com/nilpntr/content-fabrique/internal/timeline/style"
)

// Avoiding the subject: a title that sits on the person in the footage is moved off them, by the same function in the renderer and in the
// editor's check, so what the check says is what the render does. The person is three regions in output pixels (package subject makes them
// from the stored subject boxes of the clips on screen): the head and the torso, which a title must never cover, and the whole body box,
// which holds both and the background beside the head. This file knows nothing about where the regions come from.

// AvoidsSubject says whether the renderer keeps this item off the subject: it does unless the item says avoid_subject false.
func (t TextItem) AvoidsSubject() bool { return t.AvoidSubject == nil || *t.AvoidSubject }

// Subject is the person in one sample of a clip, in pixels of the output: the head and shoulders (Head), the whole person (Body; it
// contains the head) and the torso (Torso: the body below the head and shoulders, across the whole width of the person, or the whole body
// when an arm is raised above the head, because what is beside the head is then an arm and not the background). A text may cover neither
// the head nor the torso; the part of the body box beside the head, at the height of the head, is background when no arm is raised, and a
// text may stand there. A Subject with no Torso is all torso (the whole Body). ShowsOff says the clip is a pose or physique shot.
type Subject struct {
	Head, Body, Torso style.Rect
	ShowsOff          bool
}

// torso is the part of the person a text must not cover besides the head: Torso, or the whole Body when it has none.
func (s Subject) torso() style.Rect {
	if s.Torso.Width() <= 0 || s.Torso.Height() <= 0 {
		return s.Body
	}
	return s.Torso
}

// Avoidance says what PlaceTextAvoiding did.
type Avoidance struct {
	// Overlapped is true when the item as its anchor or position places it (at its own size) covers the head or the torso.
	Overlapped bool
	// Cleared is true when the item as placed covers neither the head nor the torso: it did not overlap, or it was moved or shrunk until it
	// did not. An item that avoids the subject and is not Cleared has no place at this time: the caller moves it in time (FitText in package
	// subject) or leaves it out. It is never drawn over the body.
	Cleared bool
	// OverHead and OverBody say what the item covers where its anchor puts it when it could not be cleared (OverBody: the torso or the head;
	// OverHead: the head). Both are false for a cleared item.
	OverHead, OverBody bool
	// Where says how it was cleared: "above" the subject, "beside" the head (over the background next to it), "below" all of it, "smaller"
	// for a smaller size where it stands, or "" for not moved. Scale is the scale it was placed at when the avoidance shrank it (0 when the
	// size is the item's own).
	Where string
	Scale float64
}

// The places a title can be put.
const (
	WhereAbove   = "above"
	WhereBeside  = "beside"
	WhereBelow   = "below"
	WhereSmaller = "smaller"
)

// avoidFactors are the shrinks tried, in order, within the allowed scale range.
var avoidFactors = []float64{1, 0.9, 0.8, 0.7, 0.6}

// avoidGapPct is the clear space kept between a title and the subject, as a percentage of the output height.
const avoidGapPct = 1.0

// covers says what a box (the ink of a text) covers of the subjects: the head, and the head or the torso (body).
func covers(box style.Rect, subs []Subject) (head, body bool) {
	for _, s := range subs {
		if box.Overlaps(s.Head) {
			head, body = true, true
		}
		if box.Overlaps(s.torso()) {
			body = true
		}
	}
	return head, body
}

// PlaceTextAvoiding places a text item like PlaceText and, when its ink covers the subject's head or torso, moves it where it does not,
// inside the safe zone. A text is never left over the body (P6-06): the order is
//
//  1. where its anchor puts it, when that is clear of the head and the torso;
//  2. above the subject (the band above the head, or above a raised hand), at the item's own size and then at smaller ones down to the
//     lowest scale the item may have (0.6 of its style's), or where it stands at a smaller size when that is already clear;
//  3. beside the head, over the background next to it (left or right, its letters above the shoulder line), at every size;
//  4. below all of the subject (the lower part of the frame), at every size, when there is room: that is background too.
//
// When none of these fits, the item is returned as PlaceText placed it, not Cleared, with what it covers there (OverHead, OverBody): it
// must not be drawn at this time (FitText in package subject moves it in time or leaves it out). An item that does not avoid the subject
// (avoid_subject false), or a call with no subjects, is PlaceText's result untouched and Cleared.
func PlaceTextAvoiding(out Output, it TextItem, subs []Subject) (TextPlacement, Avoidance, error) {
	pl, err := PlaceText(out, it)
	if err != nil {
		return pl, Avoidance{}, err
	}
	if !it.AvoidsSubject() || len(subs) == 0 {
		return pl, Avoidance{Cleared: true}, nil
	}
	box, ok := pl.Zone.Fit(pl.Box)
	if !ok {
		return pl, Avoidance{Cleared: true}, nil // it cannot be put inside the zone at all (unsafe_text; the renderer leaves it out)
	}
	firstHead, firstBody := covers(pl.Layout.InkBox(box), subs)
	if !firstBody {
		return pl, Avoidance{Cleared: true}, nil
	}
	gap := int(math.Round(avoidGapPct / 100 * float64(out.Height)))
	base := 1.0
	if it.Scale != nil {
		base = *it.Scale
	}
	// The scales to try: the item's own, then smaller ones down to the lowest allowed.
	var scales []float64
	seen := map[float64]bool{}
	for _, f := range avoidFactors {
		if sc := math.Max(math.Round(base*f*100)/100, style.MinScale); !seen[sc] {
			seen[sc] = true
			scales = append(scales, sc)
		}
	}
	// The placement at each scale, measured once. The first is the item's own size, whatever the rounding of the scales did to it.
	type sized struct {
		pl      TextPlacement
		applied float64 // the scale it was placed at when that is not the item's own, else 0
		ok      bool
	}
	placed := make([]sized, len(scales))
	for i, sc := range scales {
		cand := it
		if i > 0 {
			cand.Scale, placed[i].applied = &sc, sc
		}
		placed[i].pl, placed[i].ok = PlaceTextOK(out, cand)
	}
	at := func(i int) (TextPlacement, float64, bool) { return placed[i].pl, placed[i].applied, placed[i].ok }
	// What is tested against the person is the ink of the text, not its layout box: the line box of a title in capitals is twice as tall as
	// its letters, and the empty half of it may be over a head without touching it. The layout box is what must stay inside the safe zone.
	inkPadded := func(cpl TextPlacement, b style.Rect) style.Rect {
		r := cpl.Layout.InkBox(b)
		r.Top -= gap
		r.Bottom += gap
		r.Left -= gap
		r.Right += gap
		return r
	}
	// withInkTop and withInkBottom place the layout box of cpl so that its ink starts at y (a gap under a subject) or ends at y (a gap above).
	withInkTop := func(cpl TextPlacement, y int) style.Rect {
		bx := cpl.Box
		top := y - (cpl.Layout.InkBox(bx).Top - bx.Top)
		return style.Rect{Left: bx.Left, Top: top, Right: bx.Right, Bottom: top + bx.Height()}
	}
	withInkBottom := func(cpl TextPlacement, y int) style.Rect {
		bx := cpl.Box
		bottom := y + (bx.Bottom - cpl.Layout.InkBox(bx).Bottom)
		return style.Rect{Left: bx.Left, Top: bottom - bx.Height(), Right: bx.Right, Bottom: bottom}
	}
	// inkZone is the safe zone for a layout box moved to nb: the documented margins are about where text may be, so it is the ink that must be
	// inside them, and the empty space of the line box above and below the letters may reach into the top and bottom margins (a title in
	// capitals has about a third of its line box above the letters). The renderer clamps with the zone the placement carries.
	inkZone := func(cpl TextPlacement, nb style.Rect) style.Zone {
		ink := cpl.Layout.InkBox(nb)
		z := cpl.Zone
		z.Inner.Top -= ink.Top - nb.Top // the empty space over the letters, or (negative) the glyphs that rise above the line box
		z.Inner.Bottom += nb.Bottom - ink.Bottom
		return z
	}
	// clearAt says whether the layout box nb of cpl is inside its (ink) zone with its ink, a gap around it, off every head and torso.
	clearAt := func(cpl TextPlacement, nb style.Rect) (style.Zone, bool) {
		z := inkZone(cpl, nb)
		if !z.Safe(nb) {
			return z, false
		}
		_, body := covers(inkPadded(cpl, nb), subs)
		return z, !body
	}
	done := func(cpl TextPlacement, nb style.Rect, z style.Zone, where string, applied float64) (TextPlacement, Avoidance, error) {
		cpl.Box, cpl.Zone = nb, z
		return cpl, Avoidance{Overlapped: true, Cleared: true, Where: where, Scale: applied}, nil
	}
	var all []style.Rect
	for _, s := range subs {
		all = append(all, s.Body, s.Head)
	}
	// 2. Above the subject, the biggest size first; where it stands at a smaller size counts at that size.
	for i := range scales {
		cpl, applied, ok := at(i)
		if !ok {
			continue
		}
		if i > 0 {
			if b, ok := cpl.Zone.Fit(cpl.Box); ok {
				if _, body := covers(inkPadded(cpl, b), subs); !body {
					return cpl, Avoidance{Overlapped: true, Cleared: true, Where: WhereSmaller, Scale: applied}, nil
				}
			}
		}
		nb := withInkBottom(cpl, obstacleTop(cpl.Box, all)-gap)
		if z, ok := clearAt(cpl, nb); ok {
			return done(cpl, nb, z, WhereAbove, applied)
		}
	}
	// 3. Beside the head, over the background next to it: the letters end a gap above the highest shoulder line (the top of the torsos) and
	// stand a gap to the left or the right of a head.
	shoulders := torsoTop(subs) - gap
	for i := range scales {
		cpl, applied, ok := at(i)
		if !ok {
			continue
		}
		for _, s := range subs {
			for _, left := range []bool{true, false} {
				nb := withInkBottom(cpl, shoulders)
				ink := cpl.Layout.InkBox(nb)
				dx := (s.Head.Right + gap) - ink.Left
				if left {
					dx = (s.Head.Left - gap) - ink.Right
				}
				nb.Left, nb.Right = nb.Left+dx, nb.Right+dx
				if z, ok := clearAt(cpl, nb); ok {
					return done(cpl, nb, z, WhereBeside, applied)
				}
			}
		}
	}
	// 4. Below all of the subject: the background under the person.
	for i := range scales {
		cpl, applied, ok := at(i)
		if !ok {
			continue
		}
		nb := withInkTop(cpl, obstacleBottom(cpl.Box, all)+gap)
		if z, ok := clearAt(cpl, nb); ok {
			return done(cpl, nb, z, WhereBelow, applied)
		}
	}
	return pl, Avoidance{Overlapped: true, OverHead: firstHead, OverBody: true}, nil
}

// torsoTop is the highest top of the subjects' torsos: the shoulder line a text beside a head must stay above.
func torsoTop(subs []Subject) int {
	top := math.MaxInt
	for _, s := range subs {
		top = min(top, s.torso().Top)
	}
	return top
}

// Cover is what an item covers of the subjects.
type Cover struct {
	Head, Body bool
}

// TextCovers says what the item covers of the subjects, placed where its anchor or position puts it (and clamped into the zone): whatever
// the item's own avoid_subject says. Body is true when the head or the torso is covered.
func TextCovers(out Output, it TextItem, subs []Subject) (Cover, error) {
	pl, err := PlaceText(out, it)
	if err != nil {
		return Cover{}, err
	}
	box, ok := pl.Zone.Fit(pl.Box)
	if !ok {
		return Cover{}, nil // it cannot be put inside the safe zone at all (unsafe_text, and the renderer leaves it out)
	}
	head, body := covers(pl.Layout.InkBox(box), subs)
	return Cover{Head: head, Body: body}, nil
}

// obstacleTop is the top of the highest obstacle that overlaps the box horizontally (the box's own top when none does).
func obstacleTop(box style.Rect, obstacles []style.Rect) int {
	top := box.Top
	first := true
	for _, o := range obstacles {
		if o.Left < box.Right && o.Right > box.Left {
			if first || o.Top < top {
				top, first = o.Top, false
			}
		}
	}
	return top
}

// obstacleBottom is the bottom of the lowest obstacle that overlaps the box horizontally.
func obstacleBottom(box style.Rect, obstacles []style.Rect) int {
	bottom := box.Bottom
	first := true
	for _, o := range obstacles {
		if o.Left < box.Right && o.Right > box.Left {
			if first || o.Bottom > bottom {
				bottom, first = o.Bottom, false
			}
		}
	}
	return bottom
}

// PlaceTextOK is PlaceText without its error: false when the item cannot be placed.
func PlaceTextOK(out Output, it TextItem) (TextPlacement, bool) {
	pl, err := PlaceText(out, it)
	return pl, err == nil
}
