package renderjobs

import (
	"testing"

	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// A graphics item is kept off the subject like a text item, so a timeline with one needs the subject boxes loaded (D-35).
func TestGraphicsItemsReadTheSubjectBoxes(t *testing.T) {
	off := false
	g := &timeline.GraphicsTrack{ID: "graphics", Items: []timeline.GraphicsItem{{ID: "g1", Template: timeline.TemplateTitlePop}}}
	spec := &timeline.Spec{Tracks: []timeline.Track{&timeline.VideoTrack{ID: "main"}, g}}
	if !hasAvoidingText(spec) {
		t.Fatal("a graphics item that avoids the subject did not ask for the subject boxes")
	}
	g.Items[0].AvoidSubject = &off
	if hasAvoidingText(spec) {
		t.Fatal("a graphics item with avoid_subject false asked for the subject boxes")
	}
}
