package main

import (
	"testing"

	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// cfctl audio check warns when it has to measure a preview with a beat guide without the audio stem; the guide exists only for
// markers inside the video.
func TestHasGuideMarkers(t *testing.T) {
	spec := func(m ...timeline.Marker) *timeline.Spec {
		return &timeline.Spec{Output: timeline.Output{FPS: 30}, Markers: m, Tracks: []timeline.Track{&timeline.VideoTrack{ID: "main", Items: []timeline.VideoItem{
			{ID: "c1", AssetID: "ast_x", Source: timeline.Source{OutMs: 2000}},
		}}}}
	}
	cases := []struct {
		name string
		spec *timeline.Spec
		want bool
	}{
		{"none", spec(), false},
		{"a beat inside", spec(timeline.Marker{Kind: timeline.MarkerBeat, AtMs: 500}), true},
		{"a drop inside", spec(timeline.Marker{Kind: timeline.MarkerDrop, AtMs: 1000}), true},
		{"a beat past the end", spec(timeline.Marker{Kind: timeline.MarkerBeat, AtMs: 2000}), false},
		{"a section", spec(timeline.Marker{Kind: timeline.MarkerSection, AtMs: 500}), false},
	}
	for _, tc := range cases {
		if got := hasGuideMarkers(tc.spec); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}
