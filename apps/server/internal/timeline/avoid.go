package timeline

import (
	"math"

	"github.com/nilpntr/content-fabrique/internal/timeline/style"
)

// Avoiding the subject: a title that sits on the person in the footage is moved off them, by the same function in the renderer and in the
// editor's check, so what the check says is what the render does. The person is two regions in output pixels (package subject makes them
// from the stored subject boxes of the clips on screen): the head, which a title must never cover, and the body, which it should not
// cover either (a physique shot shows nothing else). This file knows nothing about where the regions come from.

// AvoidsSubject says whether the renderer keeps this item off the subject: it does unless the item says avoid_subject false.
func (t TextItem) AvoidsSubject() bool { return t.AvoidSubject == nil || *t.AvoidSubject }

// Subject is the person in one sample of a clip, in pixels of the output: the head and shoulders (Head, the hard obstacle) and the whole
// person (Body, the soft one; it contains the head). ShowsOff says the clip is a pose or physique shot, where the body is the picture.
type Subject struct {
	Head, Body style.Rect
	ShowsOff   bool
}

// Avoidance says what PlaceTextAvoiding did.
type Avoidance struct {
	// Overlapped is true when the item as its anchor or position places it (at its own size) covers the head or the body.
	Overlapped bool
	// Cleared is true when the item as placed covers neither the head nor the body: it did not overlap, or it was moved or shrunk until it
	// did not.
	Cleared bool
	// OverHead is true when the item as placed still covers the head (it could not be put anywhere inside the safe zone that does not).
	// OverBody is true when it still covers the body, the head included: a title that could only be kept off the head has OverBody and not
	// OverHead.
	OverHead, OverBody bool
	// Where says how it was cleared: "above" the subject, "below" all of it, "smaller" for a smaller size where it stands, "below_head" for the
	// last resort, under the head and over the body, or "" for not moved. Scale is the scale it was placed at when the avoidance shrank it
	// (0 when the size is the item's own).
	Where string
	Scale float64
}

// The places a title can be put.
const (
	WhereAbove     = "above"
	WhereBelow     = "below"
	WhereSmaller   = "smaller"
	WhereBelowHead = "below_head"
)

// avoidFactors are the shrinks tried, in order, within the allowed scale range.
var avoidFactors = []float64{1, 0.9, 0.8, 0.7, 0.6}

// avoidGapPct is the clear space kept between a title and the subject, as a percentage of the output height.
const avoidGapPct = 1.0

// covers says what a box (the ink of a text) covers of the subjects.
func covers(box style.Rect, subs []Subject) (head, body bool) {
	for _, s := range subs {
		if box.Overlaps(s.Head) {
			head, body = true, true
		}
		if box.Overlaps(s.Body) {
			body = true
		}
	}
	return head, body
}

// PlaceTextAvoiding places a text item like PlaceText and, when its box covers the subject, moves it where it does not, inside the safe
// zone. The order is the one that keeps a title off the person and off the body:
//
//  1. where its anchor puts it, when that is clear of the whole subject;
//  2. above the subject (the band above the head), at the item's own size and then at smaller ones down to the lowest scale the item may
//     have (0.6 of its style's), or where it stands at a smaller size when that is already clear of the subject;
//  3. below all of the subject (the lower part of the frame), at every size, when there is room;
//  4. as the last resort, off the head but over the body: where it stands when that clears the head, else just below the head. The result
//     says OverBody, and the renderer and the check report it.
//
// A title is never put just below the head over the torso while a smaller one would fit above it. When even the head cannot be cleared, it
// returns the item as PlaceText placed it and OverHead. An item that does not avoid the subject, or a call with no subjects, is PlaceText's
// result untouched.
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
	if _, body := covers(pl.Layout.InkBox(box), subs); !body {
		return pl, Avoidance{Cleared: true}, nil
	}
	res := Avoidance{Overlapped: true}
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
		return r
	}
	// withInkTop and withInkBottom place the layout box of cpl so that its ink starts at y (a gap under a subject) or ends at y (a gap above).
	withInkTop := func(cpl TextPlacement, y int) style.Rect {
		bx := cpl.Box
		top := y - (cpl.Layout.InkBox(bx).Top - bx.Top)
		return style.Rect{Left: bx.Left, Top: top, Right: bx.Right, Bottom: top + bx.Height()}
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
	withInkBottom := func(cpl TextPlacement, y int) style.Rect {
		bx := cpl.Box
		bottom := y + (bx.Bottom - cpl.Layout.InkBox(bx).Bottom)
		return style.Rect{Left: bx.Left, Top: bottom - bx.Height(), Right: bx.Right, Bottom: bottom}
	}
	var all, heads []style.Rect
	for _, s := range subs {
		all, heads = append(all, s.Body, s.Head), append(heads, s.Head)
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
		z := inkZone(cpl, nb)
		if _, body := covers(cpl.Layout.InkBox(nb), subs); z.Safe(nb) && !body {
			cpl.Box, cpl.Zone = nb, z
			return cpl, Avoidance{Overlapped: true, Cleared: true, Where: WhereAbove, Scale: applied}, nil
		}
	}
	// 3. Below all of the subject.
	for i := range scales {
		cpl, applied, ok := at(i)
		if !ok {
			continue
		}
		nb := withInkTop(cpl, obstacleBottom(cpl.Box, all)+gap)
		z := inkZone(cpl, nb)
		if _, body := covers(cpl.Layout.InkBox(nb), subs); z.Safe(nb) && !body {
			cpl.Box, cpl.Zone = nb, z
			return cpl, Avoidance{Overlapped: true, Cleared: true, Where: WhereBelow, Scale: applied}, nil
		}
	}
	// 4. Off the head, over the body: where it stands (at its own size, then smaller) or just under the head.
	for i := range scales {
		cpl, applied, ok := at(i)
		if !ok {
			continue
		}
		if b, ok := cpl.Zone.Fit(cpl.Box); ok {
			if head, _ := covers(inkPadded(cpl, b), subs); !head {
				where := ""
				if i > 0 {
					where = WhereSmaller
				}
				return cpl, Avoidance{Overlapped: true, OverBody: true, Where: where, Scale: applied}, nil
			}
		}
	}
	for i := range scales {
		cpl, applied, ok := at(i)
		if !ok {
			continue
		}
		nb := withInkTop(cpl, obstacleBottom(cpl.Box, heads)+gap)
		z := inkZone(cpl, nb)
		if head, _ := covers(cpl.Layout.InkBox(nb), subs); z.Safe(nb) && !head {
			cpl.Box, cpl.Zone = nb, z
			return cpl, Avoidance{Overlapped: true, OverBody: true, Where: WhereBelowHead, Scale: applied}, nil
		}
	}
	res.OverHead, res.OverBody = true, true
	return pl, res, nil
}

// Cover is what an item covers of the subjects.
type Cover struct {
	Head, Body bool
}

// TextCovers says what the item covers of the subjects, placed where its anchor or position puts it (and clamped into the zone): whatever
// the item's own avoid_subject says. Body is true when the head is covered too.
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
