package timeline

import "testing"

func TestShotsNumberTheClipsInPlayingOrder(t *testing.T) {
	two := 2.0
	spec := &Spec{Tracks: []Track{&VideoTrack{Items: []VideoItem{
		// Out of order in the track, as an edit may leave them; the shots follow at_ms.
		{ID: "c", AssetID: "a2", AtMs: 3000, Source: Source{InMs: 10_000, OutMs: 12_000}},
		{ID: "a", AssetID: "a1", AtMs: 0, Source: Source{InMs: 500, OutMs: 2_500}},
		// Speed 2: 2 s of source plays for 1 s.
		{ID: "b", AssetID: "a1", AtMs: 2000, Speed: &two, Source: Source{InMs: 4_000, OutMs: 6_000}},
		// Plays nothing: not a shot.
		{ID: "x", AssetID: "a3", AtMs: 5000, Source: Source{InMs: 100, OutMs: 100}},
	}}}}
	got := Shots(spec)
	want := []Shot{
		{Number: 1, ItemID: "a", AssetID: "a1", AtMs: 0, EndMs: 2000, Source: Source{InMs: 500, OutMs: 2_500}},
		{Number: 2, ItemID: "b", AssetID: "a1", AtMs: 2000, EndMs: 3000, Source: Source{InMs: 4_000, OutMs: 6_000}},
		{Number: 3, ItemID: "c", AssetID: "a2", AtMs: 3000, EndMs: 5000, Source: Source{InMs: 10_000, OutMs: 12_000}},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d shots, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("shot %d: got %+v, want %+v", i+1, got[i], want[i])
		}
	}
	if s, ok := ShotByNumber(spec, 2); !ok || s.ItemID != "b" {
		t.Errorf("ShotByNumber(2) = %+v, %v", s, ok)
	}
	if _, ok := ShotByNumber(spec, 4); ok {
		t.Error("ShotByNumber(4) found a shot that is not there")
	}
	if Shots(nil) != nil || Shots(&Spec{}) != nil {
		t.Error("a spec without a main track has shots")
	}
}

func TestAShotThatATransitionOverlapsEndsWhereTheNextStarts(t *testing.T) {
	spec := &Spec{Tracks: []Track{&VideoTrack{Items: []VideoItem{
		{ID: "a", AssetID: "a1", AtMs: 0, Source: Source{InMs: 0, OutMs: 2_250}},
		{ID: "b", AssetID: "a1", AtMs: 2000, Source: Source{InMs: 5_000, OutMs: 7_000}, TransitionIn: &Transition{Type: "crossfade", DurationMs: 250}},
	}}}}
	got := Shots(spec)
	if got[0].EndMs != 2000 || got[1].AtMs != 2000 || got[1].EndMs != 4000 {
		t.Errorf("shots %+v", got)
	}
}
