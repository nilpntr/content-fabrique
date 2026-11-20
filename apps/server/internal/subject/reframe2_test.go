package subject

import (
	"testing"

	"github.com/nilpntr/content-fabrique/internal/timeline"
)

func TestForCropIgnoresDistantSamples(t *testing.T) {
	tr := Track{Aspect: 16.0 / 9.0, Samples: []Sample{{AtMs: 0, X: 0.1, Y: 0.2, W: 0.2, H: 0.6}, {AtMs: 9000, X: 0.7, Y: 0.2, W: 0.2, H: 0.6}}}
	if got := tr.ForCrop(timeline.Source{InMs: 4000, OutMs: 5000}); len(got) != 0 {
		t.Errorf("a clip 4 s from every sample has %d", len(got))
	}
	if got := tr.ForCrop(timeline.Source{InMs: 500, OutMs: 1000}); len(got) != 1 || got[0].AtMs != 0 {
		t.Errorf("a clip 500 ms from a sample: %+v", got)
	}
	if got := WithFocus(nil, &tr, timeline.Source{InMs: 4000, OutMs: 5000}, vertical); got.Focus != nil {
		t.Error("a focus was guessed from a distant sample")
	}
}

func TestCentreIsAWholeBoxForAWalk(t *testing.T) {
	// A walk from left to right with the box moving down the frame: x and y come from one box, not two medians.
	tr := Track{Aspect: 16.0 / 9.0, Samples: []Sample{
		{AtMs: 0, X: 0.05, Y: 0.1, W: 0.1, H: 0.5}, {AtMs: 500, X: 0.45, Y: 0.4, W: 0.1, H: 0.5}, {AtMs: 900, X: 0.85, Y: 0.1, W: 0.1, H: 0.5},
	}}
	x, y, _ := tr.Centre(timeline.Source{OutMs: 1000})
	if x < 0.499 || x > 0.501 || y < 0.649 || y > 0.651 {
		t.Errorf("centre = %v %v, want the middle box (0.5, 0.65)", x, y)
	}
}

// Every reader of the automatic focus uses the clip's whole source range: the title placement, asked about a part of the clip, puts the
// person where the renderer's crop does.
func TestRegionsAgreeWithTheClipFocus(t *testing.T) {
	tr := Track{Aspect: 16.0 / 9.0, Samples: []Sample{
		{AtMs: 500, X: 0.05, Y: 0.2, W: 0.2, H: 0.7, HeadTopY: 0.22}, {AtMs: 2500, X: 0.45, Y: 0.2, W: 0.2, H: 0.7, HeadTopY: 0.22}, {AtMs: 4500, X: 0.75, Y: 0.2, W: 0.2, H: 0.7, HeadTopY: 0.22},
	}}
	src := timeline.Source{InMs: 0, OutMs: 5000}
	spec := &timeline.Spec{Output: vertical}
	spec.Tracks = []timeline.Track{&timeline.VideoTrack{ID: "main", Items: []timeline.VideoItem{{ID: "c1", AssetID: "a", Source: src, AtMs: 0}}}}
	lookup := func(string) (Track, bool) { return tr, true }
	rf := WithFocus(nil, &tr, src, vertical)
	if rf.Focus == nil || rf.Focus.X < 0.549 || rf.Focus.X > 0.551 {
		t.Fatalf("clip focus: %+v", rf.Focus)
	}
	// A title over the first second sees only the first sample, but the crop is the clip's.
	got := Regions(spec, lookup, 0, 1000)
	if len(got) != 1 {
		t.Fatalf("%d regions", len(got))
	}
	x0, y0, x1, y1 := tr.Samples[0].Body()
	if want := MapBox(x0, y0, x1, y1, tr.Aspect, rf, vertical); got[0].Body != want {
		t.Errorf("title placement body %+v, the clip's crop says %+v", got[0].Body, want)
	}
}
