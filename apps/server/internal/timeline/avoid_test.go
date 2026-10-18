package timeline

import (
	"testing"

	"github.com/nilpntr/content-fabrique/internal/timeline/style"
)

func boolp(v bool) *bool { return &v }

// person is a standing person in a 9:16 frame: the head and shoulders from headTop down 300 px, the body from headTop to near the bottom.
func person(headTop int) Subject {
	return Subject{
		Head: style.Rect{Left: 346, Top: headTop, Right: 734, Bottom: headTop + 296},
		Body: style.Rect{Left: 160, Top: headTop, Right: 920, Bottom: 1700},
	}
}

// the person with the head at 22 % of the height (y 422).
var standing = []Subject{person(422)}

func titleItem(text string) TextItem {
	return TextItem{ID: "t1", AtMs: 0, DurationMs: 1000, Text: text, Style: "title_series", Anchor: AnchorUpperThird}
}

// off checks a placement keeps the ink of the text off every region of the subjects, and the layout box inside the zone it carries.
func off(t *testing.T, name string, pl TextPlacement, subs []Subject, head bool) {
	t.Helper()
	ink := pl.Layout.InkBox(pl.Box)
	for _, s := range subs {
		if ink.Overlaps(s.Head) {
			t.Errorf("%s: the ink %+v is on the head %+v", name, ink, s.Head)
		}
		if !head && ink.Overlaps(s.Body) {
			t.Errorf("%s: the ink %+v is on the body %+v", name, ink, s.Body)
		}
	}
	if !pl.Zone.Safe(pl.Box) {
		t.Errorf("%s: the box %+v is not safe in the zone %+v", name, pl.Box, pl.Zone.Inner)
	}
}

func TestAvoidingTheSubject(t *testing.T) {
	out := baseSpec().Output
	// One line at the top of the safe zone clears the head: nothing moves.
	pl, av, err := PlaceTextAvoiding(out, titleItem("BUILDING BACK 101"), standing)
	if err != nil || av.Overlapped || !av.Cleared || av.Where != "" || av.OverBody || av.OverHead {
		t.Fatalf("one line: %+v %v", av, err)
	}
	plain, _ := PlaceText(out, titleItem("BUILDING BACK 101"))
	if pl.Box != plain.Box {
		t.Errorf("an item that clears the head was moved: %+v against %+v", pl.Box, plain.Box)
	}

	// Two lines at the top of the zone run into the head: they are cleared above the person (at a smaller size, there is no room for more),
	// off the head and the body.
	two := titleItem("BACK DAY\nBLUEPRINT")
	pl, av, err = PlaceTextAvoiding(out, two, standing)
	if err != nil || !av.Overlapped || !av.Cleared || av.Where == "" || av.OverBody {
		t.Fatalf("two lines: %+v %v", av, err)
	}
	off(t, "two lines", pl, standing, false)
	if av.Scale != 0 && (av.Scale < style.MinScale || av.Scale >= 1) {
		t.Errorf("scale %v", av.Scale)
	}

	// The switch: an item that does not avoid the subject is placed where its anchor puts it.
	no := two
	no.AvoidSubject = boolp(false)
	pl, av, err = PlaceTextAvoiding(out, no, standing)
	raw, _ := PlaceText(out, no)
	if err != nil || av.Overlapped || pl.Box != raw.Box {
		t.Errorf("avoid_subject false: %+v %+v", av, pl.Box)
	}
	// And one that says true is the default.
	on := two
	on.AvoidSubject = boolp(true)
	if _, av, _ := PlaceTextAvoiding(out, on, standing); !av.Overlapped {
		t.Errorf("avoid_subject true: %+v", av)
	}
	// No subjects: PlaceText's result.
	if _, av, _ := PlaceTextAvoiding(out, two, nil); av.Overlapped || !av.Cleared {
		t.Errorf("no subjects: %+v", av)
	}

	// Nowhere to go: a subject that fills the whole frame leaves no clear band, above or below, at any allowed size, and the head is covered.
	wall := []Subject{{Head: style.Rect{Left: 0, Top: 0, Right: 1080, Bottom: 1920}, Body: style.Rect{Left: 0, Top: 0, Right: 1080, Bottom: 1920}}}
	pl, av, err = PlaceTextAvoiding(out, two, wall)
	if err != nil || !av.Overlapped || av.Cleared || !av.OverHead || !av.OverBody {
		t.Errorf("a wall: %+v %v", av, err)
	}
	if raw, _ := PlaceText(out, two); pl.Box != raw.Box {
		t.Errorf("an item that could not be cleared was moved anyway")
	}
	// An error of PlaceText is an error.
	bad := two
	bad.Style = "no_such_style"
	if _, _, err := PlaceTextAvoiding(out, bad, standing); err == nil {
		t.Error("an unknown style was placed")
	}
}

// The hook of the back-day video: the head top is at 17 % of the frame and the band above it inside the safe zone is 86 px, too small
// for the title at its size. The title goes above the head at a smaller size: it is not put just below the head, on the back, while a
// smaller one fits above it.
func TestATitleGoesAboveTheHeadSmallerNotOntoTheBack(t *testing.T) {
	out := baseSpec().Output
	headTop := 326 // 17 % of 1920
	subs := []Subject{person(headTop)}
	it := titleItem("BACK DAY 101")
	pl, av, err := PlaceTextAvoiding(out, it, subs)
	if err != nil || !av.Overlapped || !av.Cleared || av.Where != WhereAbove || av.OverBody {
		t.Fatalf("%+v %v", av, err)
	}
	if av.Scale < style.MinScale || av.Scale >= 1 {
		t.Errorf("shrunk to %v", av.Scale)
	}
	ink := pl.Layout.InkBox(pl.Box)
	if ink.Bottom >= headTop || ink.Top < 240 {
		t.Errorf("the ink %+v is not in the band above the head (the safe zone starts at 240, the head at %d)", ink, headTop)
	}
	off(t, "above", pl, subs, false)
	// The line box reaches into the top margin by the empty space over the letters: the letters are inside the documented margin.
	if pl.Box.Top >= 240 {
		t.Logf("the box %+v did not need the margin", pl.Box)
	}

	// With the head a little lower (360) the title keeps its size.
	pl, av, _ = PlaceTextAvoiding(out, it, []Subject{person(360)})
	if !av.Cleared || av.Where != WhereAbove || av.Scale != 0 {
		t.Errorf("a head at 360: %+v", av)
	}
	off(t, "head at 380", pl, []Subject{person(360)}, false)
}

// When even a title at the lowest scale does not fit above the head, beside it or below all of the subject, it is not put over the body
// (P6-06): it is not cleared, and the result says what it would cover where it stands, for the caller to move it in time or leave it out.
func TestNeverOverTheBody(t *testing.T) {
	out := baseSpec().Output
	subs := []Subject{person(280)} // 14.6 %: 40 px of band above the head, and the person fills the width at the head
	subs[0].Head.Left, subs[0].Head.Right = 120, 960
	it := titleItem("BACK DAY 101")
	pl, av, err := PlaceTextAvoiding(out, it, subs)
	if err != nil || !av.Overlapped || av.Cleared || !av.OverBody || av.Where != "" {
		t.Fatalf("%+v %v", av, err)
	}
	if plain, _ := PlaceText(out, it); pl.Box != plain.Box {
		t.Errorf("an item that could not be cleared was moved anyway: %+v", pl.Box)
	}
}

// Gino's footage (the golden boxes of P6-06): the head top at 15 to 20 % of the frame, the torso filling the middle of it. Every title is
// placed off the head and the torso, or not at all: never over the body.
func TestGinoLikeSubjectsKeepTheTorsoClear(t *testing.T) {
	out := baseSpec().Output
	h := float64(out.Height)
	w := float64(out.Width)
	// A subject box of fractions (x, y, w, h) with the head top at hy, as the quick look reads it, in output pixels the way package subject
	// maps it (a cover 9:16 clip on a 9:16 output: the identity): the head is the middle 60 % from the head top down 22 % of the box, the
	// torso the rest of the box below it.
	mk := func(x, y, bw, bh, hy float64) Subject {
		px := func(fx, fy float64) (int, int) { return int(fx * w), int(fy * h) }
		cx, half := x+bw/2, bw*0.3
		hl, ht := px(cx-half, hy)
		hr, hb := px(cx+half, hy+bh*0.22)
		bl, bt := px(x, hy)
		br, bb := px(x+bw, y+bh)
		return Subject{Head: style.Rect{Left: hl, Top: ht, Right: hr, Bottom: hb}, Body: style.Rect{Left: bl, Top: bt, Right: br, Bottom: bb},
			Torso: style.Rect{Left: bl, Top: hb, Right: br, Bottom: bb}}
	}
	cases := []struct {
		name string
		sub  Subject
		text string
		want []string // the acceptable places; "none" for no place
	}{
		{"back pose, head at 17 %", mk(0.22, 0.16, 0.56, 0.80, 0.17), "BACK DAY 101", []string{WhereAbove}},
		{"row from behind, head at 20 %", mk(0.25, 0.19, 0.5, 0.76, 0.20), "BACK DAY 101", []string{WhereAbove}},
		{"lat pulldown, head at 15 %, narrow", mk(0.38, 0.14, 0.24, 0.84, 0.15), "1. ROWS", []string{WhereBeside}},
		{"close, head at 15 %, wide", mk(0.05, 0.14, 0.9, 0.86, 0.15), "3 ESSENTIALS VOOR EEN VOLLERE RUG", []string{WhereAbove, WhereBeside, "none"}},
		{"two lines, head at 18 %", mk(0.2, 0.17, 0.6, 0.8, 0.18), "BACK DAY\nBLUEPRINT", []string{WhereAbove, WhereBeside, "none"}},
	}
	for _, c := range cases {
		subs := []Subject{c.sub}
		pl, av, err := PlaceTextAvoiding(out, titleItem(c.text), subs)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		got := av.Where
		if !av.Cleared {
			got = "none"
		}
		ok := false
		for _, w := range c.want {
			ok = ok || w == got || (w == WhereAbove && got == "" && !av.Overlapped)
		}
		if !ok {
			t.Errorf("%s: placed %q (%+v), want one of %v", c.name, got, av, c.want)
		}
		if av.Cleared {
			ink := pl.Layout.InkBox(pl.Box)
			if ink.Overlaps(c.sub.Head) || ink.Overlaps(c.sub.Torso) {
				t.Errorf("%s: the ink %+v covers the head %+v or the torso %+v", c.name, ink, c.sub.Head, c.sub.Torso)
			}
			if !pl.Zone.Safe(pl.Box) {
				t.Errorf("%s: the box %+v is not inside the zone", c.name, pl.Box)
			}
		}
	}
}

// Beside the head: a short label next to a small head, over the background, when there is no room above and the person fills the bottom.
func TestBesideTheHead(t *testing.T) {
	out := baseSpec().Output
	s := Subject{
		Head:  style.Rect{Left: 640, Top: 250, Right: 900, Bottom: 560},
		Body:  style.Rect{Left: 560, Top: 250, Right: 980, Bottom: 1900},
		Torso: style.Rect{Left: 560, Top: 560, Right: 980, Bottom: 1900},
	}
	it := titleItem("ROWS")
	pl, av, err := PlaceTextAvoiding(out, it, []Subject{s})
	if err != nil || !av.Cleared {
		t.Fatalf("%+v %v", av, err)
	}
	if av.Where != WhereBeside && av.Where != WhereAbove && av.Where != "" {
		t.Errorf("placed %q", av.Where)
	}
	ink := pl.Layout.InkBox(pl.Box)
	if ink.Overlaps(s.Head) || ink.Overlaps(s.Torso) {
		t.Errorf("the ink %+v covers the person", ink)
	}
	// A raised arm: the torso is the whole body box, so there is no background beside the head.
	arms := s
	arms.Body.Top, arms.Torso = 100, arms.Body
	arms.Torso.Top = 100
	_, av, _ = PlaceTextAvoiding(out, it, []Subject{arms})
	if av.Where == WhereBeside {
		t.Errorf("put beside the head over a raised arm: %+v", av)
	}
}

// Below all of the subject, in the lower part of the frame, when it is clear and there is no room above.
func TestBelowTheWholeSubjectWhenThereIsNoRoomAbove(t *testing.T) {
	out := baseSpec().Output
	// A person who ends at 700 px (a close-up that stops at the chest) and starts too high for a title above.
	s := Subject{Head: style.Rect{Left: 346, Top: 280, Right: 734, Bottom: 480}, Body: style.Rect{Left: 160, Top: 280, Right: 920, Bottom: 700}}
	pl, av, err := PlaceTextAvoiding(out, titleItem("BACK DAY 101"), []Subject{s})
	if err != nil || !av.Cleared || av.Where != WhereBelow || av.OverBody {
		t.Fatalf("%+v %v", av, err)
	}
	if pl.Layout.InkBox(pl.Box).Top <= s.Body.Bottom {
		t.Errorf("not below the body: %+v", pl.Box)
	}
	off(t, "below", pl, []Subject{s}, false)
}

// A head clear and the body covered: where there is no room above or below, the title stays where it is (over the shoulders) and the result
// says so.
func TestAHeadClearAndTheBodyCovered(t *testing.T) {
	out := baseSpec().Output
	// The head is a small box in the middle; the body is wide and tall, from 300 to the bottom of the frame: a title at the top covers the body
	// at the sides of the head only.
	s := Subject{Head: style.Rect{Left: 500, Top: 600, Right: 580, Bottom: 700}, Body: style.Rect{Left: 100, Top: 300, Right: 980, Bottom: 1800}}
	it := titleItem("BACK DAY 101")
	c, err := TextCovers(out, it, []Subject{s})
	if err != nil || c.Head || !c.Body {
		t.Fatalf("covers %+v %v", c, err)
	}
	pl, av, err := PlaceTextAvoiding(out, it, []Subject{s})
	if err != nil || av.Cleared || av.OverHead || !av.OverBody || av.Where != "" {
		t.Fatalf("%+v %v", av, err)
	}
	plain, _ := PlaceText(out, it)
	if pl.Box != plain.Box {
		t.Errorf("the title moved though the head was clear: %+v", pl.Box)
	}
}

// Above the person at the size of the item when the band fits it, for a title lower in the frame.
func TestAvoidingPlacesAboveWhenTheBandFits(t *testing.T) {
	out := baseSpec().Output
	high := []Subject{person(820)} // a head far down the frame: a wide band above it
	it := titleItem("BACK DAY")
	it.Anchor = AnchorCenter // at the vertical middle of the frame, over the head
	pl, av, err := PlaceTextAvoiding(out, it, high)
	if err != nil || !av.Overlapped || !av.Cleared || av.Where != WhereAbove || av.Scale != 0 {
		t.Fatalf("%+v %v", av, err)
	}
	off(t, "above", pl, high, false)
}

// A title moved above the person keeps its ink inside the documented top margin even when a glyph rises above the capitals (a bracket, a
// bar, a ring accent) or above the line box itself (a stacked accent in Inter): the ink box says how far each can rise.
func TestTheInkOfATallGlyphStaysOutOfTheTopMargin(t *testing.T) {
	out := baseSpec().Output
	subs := []Subject{person(380)}
	above := 0
	for _, c := range []struct{ style, text string }{
		{"title_series", "(BACK) DAY 101"}, {"title_series", "|BACK DAY|"}, {"title_series", "ÅNGSTRÖM"}, {"title_series", "Ǻ DAY"},
		{"title_lowercase", "back day |"}, {"title_lowercase", "Ǻ Ế day"}, {"subtitle_clean", "(row) | pull"},
	} {
		it := titleItem(c.text)
		it.Style = c.style
		pl, av, err := PlaceTextAvoiding(out, it, subs)
		if err != nil {
			t.Fatal(err)
		}
		ink := pl.Layout.InkBox(pl.Box)
		if av.Where == WhereAbove {
			above++
		}
		if av.Where == WhereAbove && ink.Top < 240 {
			t.Errorf("%s %q: the ink box %+v starts above the margin (240)", c.style, c.text, ink)
		}
		if av.Where == WhereAbove && ink.Bottom >= 380 {
			t.Errorf("%s %q: the ink box %+v reaches the head (380)", c.style, c.text, ink)
		}
	}
	if above < 2 {
		t.Errorf("only %d of the cases were put above the head: the test does not test the margin", above)
	}
}

// An item that cannot be put inside the safe zone at all is not "over the subject": the renderer leaves it out (unsafe_text), and the two
// functions agree.
func TestAnItemThatDoesNotFitTheZoneIsNotOverTheSubject(t *testing.T) {
	out := baseSpec().Output
	huge := titleItem("A\nB\nC")
	huge.Scale = ptr(1.4)
	huge.Style = "stat_big" // 11 % of the height a line: three lines are taller than the zone
	wall := []Subject{{Head: style.Rect{Left: 0, Top: 0, Right: 1080, Bottom: 1920}, Body: style.Rect{Left: 0, Top: 0, Right: 1080, Bottom: 1920}}}
	c, err := TextCovers(out, huge, wall)
	if err != nil || c.Head || c.Body {
		t.Errorf("a text that does not fit: %+v, %v", c, err)
	}
	if _, av, _ := PlaceTextAvoiding(out, huge, wall); av.Overlapped {
		t.Errorf("%+v", av)
	}
	// One that fits and is over: true.
	if c, _ := TextCovers(out, titleItem("BACK DAY\nBLUEPRINT"), standing); !c.Head || !c.Body {
		t.Errorf("two lines on the head: %+v", c)
	}
}
