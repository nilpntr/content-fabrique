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

// When even a title at the lowest scale does not fit above the head, and there is no room below all of the subject, the last resort is
// under the head over the body, and it is said so: OverBody, not OverHead.
func TestOverTheBodyIsTheLastResort(t *testing.T) {
	out := baseSpec().Output
	subs := []Subject{person(280)} // 14.6 %: 40 px of band above the head
	pl, av, err := PlaceTextAvoiding(out, titleItem("BACK DAY 101"), subs)
	if err != nil || !av.Overlapped || av.Cleared || av.OverHead || !av.OverBody || av.Where != WhereBelowHead {
		t.Fatalf("%+v %v", av, err)
	}
	off(t, "below the head", pl, subs, true)
	if pl.Layout.InkBox(pl.Box).Top <= subs[0].Head.Bottom {
		t.Errorf("not under the head: %+v", pl.Box)
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
