package editor

import (
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// list_footage and find_moments show a clip's labels from the vocabulary, in its words, in place of the old free-form tags; the
// exercise name is model text and only list_footage prints it, as untrusted data.
func TestTheEditorSeesTheLabels(t *testing.T) {
	dur, w, h := int64(12_000), int32(1080), int32(1920)
	var a, b ids.AssetID
	a.UUID[0], b.UUID[0] = 1, 2
	rows := []store.ProjectFootageRow{
		{AssetID: a, Kind: store.AssetKindVideo, Title: "Seated cable row", Exercise: "seated cable row", DurationMs: &dur, Width: &w, Height: &h,
			Facets: []string{"type:lift", "body:back", "movement:row", "angle:side", "equipment:cable", "nonsense"}},
		{AssetID: b, Kind: store.AssetKindVideo, Title: "IMG_0001", DurationMs: &dur, Width: &w, Height: &h},
	}
	full := footageText(rows, footageOpts{Full: true})
	if !strings.Contains(full, "  labels: Lifting, Back, Row, Side view, Cable\n") || strings.Contains(full, "nonsense") {
		t.Errorf("list_footage:\n%s", full)
	}
	if !strings.Contains(full, `exercise: <untrusted_data kind="clip_exercise">`) || strings.Count(full, "labels:") != 1 {
		t.Errorf("the exercise is untrusted and a clip without labels has no line:\n%s", full)
	}
	brief := footageText(rows, footageOpts{Detailed: 2, TitleChars: 40})
	if !strings.Contains(brief, "labels: Lifting, Back, Row, Side view, Cable") || strings.Contains(brief, "exercise:") {
		t.Errorf("the overview keeps the labels and leaves the exercise to list_footage:\n%s", brief)
	}

	line := momentClipLabels(rows, []store.Moment{{AssetID: a}, {AssetID: a}, {AssetID: b}})
	if line != "Clip labels:\n- "+a.String()+": Lifting, Back, Row, Side view, Cable\n" {
		t.Errorf("find_moments labels = %q", line)
	}
	if momentClipLabels(rows, []store.Moment{{AssetID: b}}) != "" {
		t.Error("a clip without labels gets no line")
	}
}
