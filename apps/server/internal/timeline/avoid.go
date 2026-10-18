package timeline

import (
	"math"
	"strings"

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

// avoided are the subjects as the item must keep off them: all of them (the head and the torso), or for an item that avoids the face
// only, the head and shoulders alone (Body and Torso are the head, so the room above, beside and below the head is free, the chest too).
func (t TextItem) avoided(subs []Subject) []Subject {
	if !t.AvoidsFaceOnly() || len(subs) == 0 {
		return subs
	}
	out := make([]Subject, len(subs))
	for i, s := range subs {
		out[i] = Subject{Head: s.Head, Body: s.Head, Torso: s.Head, ShowsOff: s.ShowsOff}
	}
	return out
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
	// size is the item's own); it may be under style.MinScale, down to the style's legibility floor (style.FitFloor).
	Where string
	Scale float64
	// Lines is how many lines the text takes when the avoidance wrapped it narrower to stack it beside the head (0 when it keeps its own
	// lines).
	Lines int
	// Tried is the room the avoidance found around the subject, for a caller that has to explain why an item could not be placed.
	Tried Tried
}

// Tried is the room PlaceTextAvoiding found around the subjects, in output pixels.
type Tried struct {
	// AboveBandPx is the tallest clear band between the top of the safe zone and the subject (a gap left out).
	AboveBandPx int
	// BesideWidthPx and BesideHeightPx are the widest room beside a head (inside the zone, a gap from the head) and how tall it is (from
	// the top of the zone to the shoulder line).
	BesideWidthPx, BesideHeightPx int
	// FloorScale is the smallest scale tried (the style's legibility floor), 0 when nothing under the item's own was tried.
	FloorScale float64
}

// The places a title can be put.
const (
	WhereAbove   = "above"
	WhereBeside  = "beside"
	WhereBelow   = "below"
	WhereSmaller = "smaller"
)

// avoidFactors are the shrinks tried first, relative to the item's own size, down to style.MinScale; under it the scales go on in steps
// of fitStep (absolute) down to the style's legibility floor.
var avoidFactors = []float64{1, 0.9, 0.8, 0.7, 0.6}

// fitStep is the step of the scales tried under style.MinScale.
const fitStep = 0.05

// MaxBesideLines is the most lines a title stacked beside a head may take.
const MaxBesideLines = 3

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

// BoxCovers says what a box covers of the subjects: the head, and the head or the torso (body). A box clear of both may stand there.
func BoxCovers(box style.Rect, subs []Subject) (head, body bool) { return covers(box, subs) }

// FitScales are the scales the avoidance tries for an item, largest first: its own, the shrinks of avoidFactors down to style.MinScale,
// then steps of fitStep down to the legibility floor of its style on this output (style.FitFloor: capitals of at least 2.2 % of the
// height, and never under style.MinFitScale). An item whose own scale is at or under the floor is only tried at its own.
func FitScales(out Output, it TextItem) []float64 {
	base := 1.0
	if it.Scale != nil {
		base = *it.Scale
	}
	floor := style.MinScale
	if st, ok := style.Lookup(it.Style); ok {
		floor = st.FitFloor(out.ShortSide(), out.Height)
	}
	floor = math.Min(floor, base)
	scales := []float64{base}
	seen := map[float64]bool{base: true}
	add := func(sc float64) {
		sc = math.Round(sc*100) / 100
		if sc >= floor && sc < base && !seen[sc] {
			seen[sc] = true
			scales = append(scales, sc)
		}
	}
	for _, f := range avoidFactors[1:] {
		add(math.Max(base*f, style.MinScale))
	}
	for sc := math.Min(style.MinScale, base) - fitStep; sc > floor+1e-9; sc -= fitStep {
		add(sc)
	}
	add(floor)
	return scales
}

// placeScaled is PlaceText at a scale the avoidance chose (absolute: relative to the style's own size), wrapped so that its box is at
// most maxBoxWidth wide (0: the zone's centre column, as PlaceText). A scale under style.MinScale is not one a spec may ask for, so the
// style is shrunk directly.
func placeScaled(out Output, it TextItem, scale float64, maxBoxWidth int) (TextPlacement, bool) {
	cand := it
	shrink := 1.0
	if scale < style.MinScale {
		cand.Scale, shrink = nil, scale
	} else {
		cand.Scale = &scale
	}
	pl, err := PlaceText(out, cand)
	if err != nil {
		return pl, false
	}
	if shrink == 1 && maxBoxWidth <= 0 {
		return pl, true
	}
	st := pl.Style
	if shrink != 1 {
		st = st.Shrunk(shrink)
	}
	if maxBoxWidth <= 0 {
		maxBoxWidth = style.MaxBoxWidth(pl.Zone, out.Width)
	}
	lay := style.Measure(st, out.ShortSide(), it.Text, maxBoxWidth)
	w, h := lay.Box()
	var box style.Rect
	if it.Position != nil {
		box = style.PlacePosition(out.Width, out.Height, it.Position.X, it.Position.Y, w, h)
	} else {
		box = style.PlaceAnchor(pl.Zone, it.Anchor, out.Width, out.Height, w, h)
	}
	pl.Style, pl.Layout, pl.Box = st, lay, box
	return pl, true
}

// wholeWords says the layout broke the text only between words (Measure breaks a word that is wider than a line between its letters,
// which a title stacked beside a head must not do).
func wholeWords(lay style.Layout, text string) bool {
	return strings.Join(strings.Fields(strings.Join(lay.Lines, " ")), " ") == strings.Join(strings.Fields(text), " ")
}

// PlaceTextAvoiding places a text item like PlaceText and, when its ink covers the subject's head or torso, moves it where it does not,
// inside the safe zone. A text is never left over the body (P6-06): the order is
//
//  1. where its anchor puts it, when that is clear of the head and the torso;
//  2. above the subject (the band above the head, or above a raised hand), at the item's own size and then at smaller ones, down to the
//     legibility floor of its style (style.FitFloor: capitals of at least 2.2 % of the frame height, and never under a scale of 0.45), or
//     where it stands at a smaller size when that is already clear;
//  3. beside the head, over the background next to it (left or right, above the shoulder line): wrapped to the width of the room there in
//     up to MaxBesideLines lines of whole words, at every size down to the floor; at the item's own height when that is clear (the top
//     band, where a title belongs), else with its letters just above the shoulders;
//  4. below all of the subject (the lower part of the frame), at every size, when there is room: that is background too.
//
// When none of these fits, the item is returned as PlaceText placed it, not Cleared, with what it covers there (OverHead, OverBody) and
// the room that was found (Tried): it must not be drawn at this time (FitText in package subject moves it in time or leaves it out). An
// item that does not avoid the subject (avoid_subject false), or a call with no subjects, is PlaceText's result untouched and Cleared.
func PlaceTextAvoiding(out Output, it TextItem, subs []Subject) (TextPlacement, Avoidance, error) {
	subs = it.avoided(subs)
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
	scales := FitScales(out, it)
	// The placement at each scale, measured once. The first is the item's own size, whatever the rounding of the scales did to it.
	type sized struct {
		pl      TextPlacement
		applied float64 // the scale it was placed at when that is not the item's own, else 0
		ok      bool
	}
	placed := make([]sized, len(scales))
	placed[0] = sized{pl: pl, ok: true}
	for i := 1; i < len(scales); i++ {
		placed[i].pl, placed[i].ok = placeScaled(out, it, scales[i], 0)
		placed[i].applied = scales[i]
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
	var tried Tried
	if len(scales) > 1 {
		tried.FloorScale = scales[len(scales)-1]
	}
	done := func(cpl TextPlacement, nb style.Rect, z style.Zone, where string, applied float64, lines int) (TextPlacement, Avoidance, error) {
		cpl.Box, cpl.Zone = nb, z
		return cpl, Avoidance{Overlapped: true, Cleared: true, Where: where, Scale: applied, Lines: lines, Tried: tried}, nil
	}
	var all []style.Rect
	for _, s := range subs {
		all = append(all, s.Body, s.Head)
	}
	zoneTop := pl.Zone.Inner.Top
	// 2. Above the subject, the biggest size first; where it stands at a smaller size counts at that size.
	for i := range scales {
		cpl, applied, ok := at(i)
		if !ok {
			continue
		}
		if i > 0 {
			if b, ok := cpl.Zone.Fit(cpl.Box); ok {
				if _, body := covers(inkPadded(cpl, b), subs); !body {
					return cpl, Avoidance{Overlapped: true, Cleared: true, Where: WhereSmaller, Scale: applied, Tried: tried}, nil
				}
			}
		}
		top := obstacleTop(cpl.Box, all)
		tried.AboveBandPx = max(tried.AboveBandPx, top-gap-zoneTop)
		nb := withInkBottom(cpl, top-gap)
		if z, ok := clearAt(cpl, nb); ok {
			return done(cpl, nb, z, WhereAbove, applied, 0)
		}
	}
	// 3. Beside the head, over the background next to it: the letters end a gap above the highest shoulder line (the top of the torsos) and
	// stand a gap to the left or the right of a head. The text is wrapped to the width of the room on that side, in whole words and at most
	// MaxBesideLines lines, so a long title stacks beside the head instead of running into it.
	shoulders := torsoTop(subs) - gap
	inner := pl.Zone.Inner
	tried.BesideHeightPx = max(shoulders-zoneTop, 0)
	ownLines := len(pl.Layout.Lines)
	for i, sc := range scales {
		applied := placed[i].applied
		for _, s := range subs {
			for _, left := range []bool{true, false} {
				room := inner.Right - (s.Head.Right + gap)
				if left {
					room = (s.Head.Left - gap) - inner.Left
				}
				tried.BesideWidthPx = max(tried.BesideWidthPx, room)
				if room <= 0 {
					continue
				}
				cpl, ok := placeScaled(out, it, sc, room)
				if !ok || len(cpl.Layout.Lines) > MaxBesideLines || !wholeWords(cpl.Layout, it.Text) || cpl.Box.Width() > room {
					continue
				}
				lines := 0
				if n := len(cpl.Layout.Lines); n != ownLines {
					lines = n
				}
				// The layout box hugs the head on its side; the ink is as wide as the box.
				hug := func(nb style.Rect) style.Rect {
					ink := cpl.Layout.InkBox(nb)
					dx := (s.Head.Right + gap) - ink.Left
					if left {
						dx = (s.Head.Left - gap) - ink.Right
					}
					nb.Left, nb.Right = nb.Left+dx, nb.Right+dx
					return nb
				}
				for _, nb := range []style.Rect{hug(cpl.Box), hug(withInkBottom(cpl, shoulders))} {
					if z, ok := clearAt(cpl, nb); ok {
						return done(cpl, nb, z, WhereBeside, applied, lines)
					}
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
			return done(cpl, nb, z, WhereBelow, applied, 0)
		}
	}
	tried.AboveBandPx = max(tried.AboveBandPx, 0)
	tried.BesideWidthPx = max(tried.BesideWidthPx, 0)
	return pl, Avoidance{Overlapped: true, OverHead: firstHead, OverBody: true, Tried: tried}, nil
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
	subs = it.avoided(subs)
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
