package render

import (
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/timeline"
)

func overridesSpec(items ...timeline.TextItem) *timeline.Spec {
	return &timeline.Spec{
		Schema: timeline.SchemaVersion,
		Output: timeline.Output{Format: timeline.Format9x16, Width: 1080, Height: 1920, FPS: 30, Background: "#000000"},
		Tracks: []timeline.Track{
			&timeline.VideoTrack{ID: "main", Items: []timeline.VideoItem{{ID: "c1", AssetID: "ast_01j3er3kwcgrasdgd4aznmxxe0", Source: timeline.Source{InMs: 0, OutMs: 6000}, AtMs: 0}}},
			&timeline.TextTrack{ID: "text", Items: items},
		},
	}
}

func f64(v float64) *float64 { return &v }

// A text item's fill, scale, shadow and align reach the ASS script as a style of their own (a colour, a size and a shadow offset
// as numbers) and as an alignment tag chosen from three fixed ones; the text of the spec stays the only free text.
func TestTextOverridesInTheScript(t *testing.T) {
	spec := overridesSpec(
		timeline.TextItem{ID: "t1", AtMs: 0, DurationMs: 1500, Text: "BUILDING LATS 101", Style: "title_series", Anchor: "upper_third", Fill: "#F2E9A0", Scale: f64(0.85), Shadow: "none"},
		timeline.TextItem{ID: "t2", AtMs: 1500, DurationMs: 1500, Text: "BRACHIORADIALIS\nBUILDER 101", Style: "title_series", Anchor: "upper_third", Align: "left"},
		timeline.TextItem{ID: "t3", AtMs: 3000, DurationMs: 1500, Text: "right\nedge", Style: "title_lowercase", Anchor: "upper_third", Align: "right", Shadow: "strong"},
		timeline.TextItem{ID: "t4", AtMs: 4500, DurationMs: 1500, Text: "plain", Style: "title_series", Anchor: "upper_third"},
	)
	plan, err := Compile(spec, Final, plainResolver())
	if err != nil {
		t.Fatal(err)
	}
	ass := plan.Subtitles.ASS
	ds := parseDialogues(t, ass)
	if len(ds) != 4 {
		t.Fatalf("%d events", len(ds))
	}
	// t1: the derived style, with the fill (&HAABBGGRR) as ASS writes it, at the smaller size and with no shadow.
	if ds[0].style != "title_series~fF2E9A0~s85~hn" {
		t.Errorf("t1 style %q", ds[0].style)
	}
	var line1, lineBase string
	for _, l := range strings.Split(ass, "\n") {
		switch {
		case strings.HasPrefix(l, "Style: title_series~fF2E9A0~s85~hn,"):
			line1 = l
		case strings.HasPrefix(l, "Style: title_series,"):
			lineBase = l
		}
	}
	if line1 == "" || lineBase == "" {
		t.Fatalf("style lines missing:\n%s", ass)
	}
	f1, fb := strings.Split(line1, ","), strings.Split(lineBase, ",")
	if f1[3] != "&H00A0E9F2" {
		t.Errorf("fill: %v against %v", f1[1:4], fb[1:4])
	}
	if f1[2] == fb[2] {
		t.Errorf("a scale of 0.85 left the font size at %s", f1[2])
	}
	// Alignment: centre for the plain ones, left and right as the fixed tags, at the ink edge of the box.
	head := regexp.MustCompile(`^\{(\\an[456])\\pos\(([0-9.]+),([0-9.]+)\)\}`)
	for i, want := range []string{`\an5`, `\an4`, `\an6`, `\an5`} {
		m := head.FindStringSubmatch(ds[i].text)
		if m == nil || m[1] != want {
			t.Errorf("event %d starts %q, want %s", i, ds[i].text, want)
		}
	}
	// The left edge is left of the centre line and the right edge right of it.
	l, r := head.FindStringSubmatch(ds[1].text), head.FindStringSubmatch(ds[2].text)
	if l == nil || r == nil || !(atof(t, l[2]) < 540 && atof(t, r[2]) > 540) {
		t.Errorf("edges: %v %v", l, r)
	}
	// Nothing of the fill or the alignment is free text: the script holds only the fixed tags.
	if strings.Contains(ass, "#F2E9A0") || strings.Contains(ass, "left") {
		t.Errorf("the spec's words reached the script outside the text:\n%s", ass)
	}
}

func atof(t *testing.T, s string) float64 {
	t.Helper()
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		t.Fatal(err)
	}
	return v
}
