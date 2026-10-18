package timeline

import (
	"os"
	"slices"
	"testing"
)

// The schema caps a graphics track at MaxGraphicsItems (cf-motion's own limit); the semantic layer holds a spec built in Go to it too.
func TestGraphicsTrackLimit(t *testing.T) {
	data, err := os.ReadFile("../../../../packages/schemas/timeline/fixtures/invalid/i85_schema_too_many_graphics.json")
	if err != nil {
		t.Fatal(err)
	}
	spec, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	issues := checkSemantics(spec, MemoryCatalog(nil))
	if !slices.ContainsFunc(issues, func(is Issue) bool { return is.Code == CodeInvalidGraphic && is.Path == "/tracks/1/items" }) {
		t.Fatalf("65 graphics items passed the semantic layer: %+v", issues)
	}
	for _, tr := range spec.Tracks {
		if g, ok := tr.(*GraphicsTrack); ok {
			g.Items = g.Items[:MaxGraphicsItems]
		}
	}
	for _, is := range checkSemantics(spec, MemoryCatalog(nil)) {
		if is.Path == "/tracks/1/items" {
			t.Fatalf("64 items refused: %+v", is)
		}
	}
}

func TestGraphicsRetimedAndTitles(t *testing.T) {
	g := GraphicsItem{ID: "g1", Template: TemplateTitlePop, AtMs: 1000, DurationMs: 2000, Params: GraphicsParams{AccentsMs: []int64{1000, 1500, 2900}}}
	if m := g.Retimed(4000, 2000, true); m.AtMs != 4000 || !slices.Equal(m.Params.AccentsMs, []int64{4000, 4500, 5900}) {
		t.Errorf("moved: %+v", m)
	}
	if s := g.Retimed(1400, 1000, false); s.AtMs != 1400 || s.DurationMs != 1000 || !slices.Equal(s.Params.AccentsMs, []int64{1500}) {
		t.Errorf("shortened: %+v", s)
	}
	if len(g.Params.AccentsMs) != 3 {
		t.Error("Retimed changed the item's own accents")
	}
	for tpl, want := range map[string]bool{TemplateTitlePop: true, TemplateBlurIn: true, TemplateChapterCard: true, TemplateCounter: false, TemplateLowerLabel: false, TemplateHighlightWord: false, TemplateOutroCard: false} {
		if (GraphicsItem{Template: tpl}).IsTitle() != want {
			t.Errorf("%s: IsTitle %v", tpl, !want)
		}
	}
}
