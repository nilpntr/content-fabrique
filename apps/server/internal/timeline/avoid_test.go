package timeline

import (
	"testing"

	"github.com/nilpntr/content-fabrique/internal/timeline/style"
)

func boolp(v bool) *bool { return &v }

// the head of a person standing in a 9:16 frame: x 346 to 734, y 422 to 718 (a head top at 22 % of the height).
var head = []style.Rect{{Left: 346, Top: 422, Right: 734, Bottom: 718}}

func titleItem(text string) TextItem {
	return TextItem{ID: "t1", AtMs: 0, DurationMs: 1000, Text: text, Style: "title_series", Anchor: AnchorUpperThird}
}

func TestAvoidingTheSubject(t *testing.T) {
	out := baseSpec().Output
	// One line at the top of the safe zone clears the head: nothing moves.
	pl, av, err := PlaceTextAvoiding(out, titleItem("BUILDING BACK 101"), head)
	if err != nil || av.Overlapped || !av.Cleared || av.Where != "" {
		t.Fatalf("one line: %+v %v", av, err)
	}
	plain, _ := PlaceText(out, titleItem("BUILDING BACK 101"))
	if pl.Box != plain.Box {
		t.Errorf("an item that clears the head was moved: %+v against %+v", pl.Box, plain.Box)
	}

	// Two lines at the top of the zone run into the head: they are cleared (above if they fit, else smaller, else below), inside the zone
	// and off the head.
	two := titleItem("BACK DAY\nBLUEPRINT")
	pl, av, err = PlaceTextAvoiding(out, two, head)
	if err != nil || !av.Overlapped || !av.Cleared || av.Where == "" {
		t.Fatalf("two lines: %+v %v", av, err)
	}
	if !pl.Zone.Safe(pl.Box) || pl.Box.Overlaps(head[0]) {
		t.Errorf("the moved box %+v is unsafe or still on the head", pl.Box)
	}
	if av.Scale != 0 && (av.Scale < style.MinScale || av.Scale >= 1) {
		t.Errorf("scale %v", av.Scale)
	}
	if av.Where == "above" && pl.Box.Bottom >= head[0].Top {
		t.Errorf("above, but the bottom is %d", pl.Box.Bottom)
	}
	if av.Where == "below" && pl.Box.Top <= head[0].Bottom {
		t.Errorf("below, but the top is %d", pl.Box.Top)
	}

	// The switch: an item that does not avoid the subject is placed where its anchor puts it.
	off := two
	off.AvoidSubject = boolp(false)
	pl, av, err = PlaceTextAvoiding(out, off, head)
	raw, _ := PlaceText(out, off)
	if err != nil || av.Overlapped || pl.Box != raw.Box {
		t.Errorf("avoid_subject false: %+v %+v", av, pl.Box)
	}
	// And one that says true is the default.
	on := two
	on.AvoidSubject = boolp(true)
	if _, av, _ := PlaceTextAvoiding(out, on, head); !av.Overlapped {
		t.Errorf("avoid_subject true: %+v", av)
	}
	// No obstacles: PlaceText's result.
	if _, av, _ := PlaceTextAvoiding(out, two, nil); av.Overlapped || !av.Cleared {
		t.Errorf("no obstacles: %+v", av)
	}

	// Nowhere to go: a subject that fills the whole safe zone leaves no clear band, above or below, at any allowed size.
	wall := []style.Rect{{Left: 0, Top: 0, Right: 1080, Bottom: 1920}}
	pl, av, err = PlaceTextAvoiding(out, two, wall)
	if err != nil || !av.Overlapped || av.Cleared {
		t.Errorf("a wall: %+v %v", av, err)
	}
	if raw, _ := PlaceText(out, two); pl.Box != raw.Box {
		t.Errorf("an item that could not be cleared was moved anyway")
	}
	// An error of PlaceText is an error.
	bad := two
	bad.Style = "no_such_style"
	if _, _, err := PlaceTextAvoiding(out, bad, head); err == nil {
		t.Error("an unknown style was placed")
	}
}

// Above the head at a smaller size is preferred to below the head at the full size: the band above the head is where a title belongs.
func TestAvoidingShrinksToFitAboveBeforeGoingBelow(t *testing.T) {
	out := baseSpec().Output
	h := []style.Rect{{Left: 346, Top: 460, Right: 734, Bottom: 760}}
	pl, av, err := PlaceTextAvoiding(out, titleItem("BACK DAY\nBLUEPRINT"), h)
	if err != nil || !av.Overlapped || !av.Cleared || av.Where != "above" || av.Scale < style.MinScale || av.Scale >= 1 {
		t.Fatalf("%+v %v", av, err)
	}
	if pl.Box.Bottom >= h[0].Top || pl.Box.Top < pl.Zone.Inner.Top {
		t.Errorf("box %+v", pl.Box)
	}
}

// A title lower in the frame, on the chest: it goes above the head if the band above fits it, else below.
func TestAvoidingPlacesAboveWhenTheBandFits(t *testing.T) {
	out := baseSpec().Output
	high := []style.Rect{{Left: 346, Top: 700, Right: 734, Bottom: 900}} // a head far down the frame: a wide band above it
	it := titleItem("BACK DAY")
	it.Anchor = AnchorCenter // at the vertical middle of the frame, over the head
	pl, av, err := PlaceTextAvoiding(out, it, high)
	if err != nil || !av.Overlapped || !av.Cleared || av.Where != "above" || av.Scale != 0 {
		t.Fatalf("%+v %v", av, err)
	}
	if pl.Box.Bottom >= high[0].Top || !pl.Zone.Safe(pl.Box) {
		t.Errorf("box %+v", pl.Box)
	}
	// With the band above too small and the zone's bottom free, it goes below.
	tight := []style.Rect{{Left: 346, Top: 260, Right: 734, Bottom: 560}}
	pl, av, err = PlaceTextAvoiding(out, titleItem("BACK DAY"), tight)
	if err != nil || !av.Cleared || av.Where != "below" || pl.Box.Top <= tight[0].Bottom {
		t.Errorf("%+v %+v %v", av, pl.Box, err)
	}
}

// An item that cannot be put inside the safe zone at all is not "over the subject": the renderer leaves it out (unsafe_text), and the two
// functions agree.
func TestAnItemThatDoesNotFitTheZoneIsNotOverTheSubject(t *testing.T) {
	out := baseSpec().Output
	huge := titleItem("A\nB\nC")
	huge.Scale = ptr(1.4)
	huge.Style = "stat_big" // 11 % of the height a line: three lines are taller than the zone
	over, err := TextOverlaps(out, huge, []style.Rect{{Left: 0, Top: 0, Right: 1080, Bottom: 1920}})
	if err != nil || over {
		t.Errorf("a text that does not fit: over %v, %v", over, err)
	}
	if _, av, _ := PlaceTextAvoiding(out, huge, []style.Rect{{Left: 0, Top: 0, Right: 1080, Bottom: 1920}}); av.Overlapped {
		t.Errorf("%+v", av)
	}
	// One that fits and is over: true.
	if over, _ := TextOverlaps(out, titleItem("BACK DAY\nBLUEPRINT"), head); !over {
		t.Error("two lines on the head")
	}
}
