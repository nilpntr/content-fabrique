package subject

import (
	"testing"

	"github.com/nilpntr/content-fabrique/internal/timeline"
	"github.com/nilpntr/content-fabrique/internal/timeline/style"
)

func captionSpec(items ...timeline.VideoItem) *timeline.Spec {
	return &timeline.Spec{
		Schema: timeline.SchemaVersion,
		Output: timeline.Output{Format: timeline.Format9x16, Width: 1080, Height: 1920, FPS: 30, Background: "#000000"},
		Tracks: []timeline.Track{&timeline.VideoTrack{ID: "main", Items: items}},
	}
}

func rows(top, bottom float64) Track {
	return Track{Aspect: 0.5625, Samples: []Sample{{AtMs: 1000, X: 0.3, Y: top, W: 0.4, H: min(0.45, 1-top), HeadTopY: top, HeadX: 0.4, HeadW: 0.2, ShoulderY: bottom}}}
}

// The captions of a clip go to the upper band when the face is in the lower one, per clip, so two clips of one video can differ.
func TestCaptionPlacesPerClip(t *testing.T) {
	st, _ := style.CaptionStyle("chunk")
	spec := captionSpec(
		timeline.VideoItem{ID: "c1", AssetID: "a", Source: timeline.Source{InMs: 0, OutMs: 2000}, AtMs: 0},
		timeline.VideoItem{ID: "c2", AssetID: "b", Source: timeline.Source{InMs: 0, OutMs: 2000}, AtMs: 2000},
		timeline.VideoItem{ID: "c3", AssetID: "c", Source: timeline.Source{InMs: 0, OutMs: 2000}, AtMs: 4000},
	)
	idx := Index{"a": rows(0.52, 0.72), "b": rows(0.2, 0.34), "c": rows(0.1, 0.97)}
	places := CaptionPlaces(spec, idx.Lookup, st)
	if len(places) != 3 {
		t.Fatalf("%d places", len(places))
	}
	if p := places[0]; p.Pos != style.CaptionUpper || !p.Moved || p.OverFace {
		t.Errorf("c1 (face in the lower band): %+v", p)
	}
	if p := places[1]; p.Pos != style.CaptionLower || p.Moved || p.OverFace {
		t.Errorf("c2 (face high): %+v", p)
	}
	if p := places[2]; !p.OverFace || p.Face == (style.Rect{}) {
		t.Errorf("c3 (face in both bands): %+v", p)
	}
	// No subject at all: everything stays low.
	for _, p := range CaptionPlaces(spec, nil, st) {
		if p.Pos != style.CaptionLower || p.Moved || p.OverFace {
			t.Errorf("without subjects: %+v", p)
		}
	}
}
