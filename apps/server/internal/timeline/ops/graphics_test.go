package ops

import (
	"testing"

	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// The random generators of the graphics ops for TestPropertyRandomBatches: every template, params that fit it and some that do not
// (an accent outside the item, a word that does not exist), anchors and positions, and updates of each field.

var graphicsTexts = []string{"BACK DAY 101", "SEATED ROW", "SQUEEZE AT THE TOP", "REPS", "how it feels", "SAVE FOR\nBACK DAY", "A VERY LONG TITLE THAT WILL WRAP OVER TWO LINES OF TEXT"}

func (g *generator) graphicsParams(template string, at, dur int64) timeline.GraphicsParams {
	p := timeline.GraphicsParams{Text: pick(g, graphicsTexts)}
	if g.chance(40) {
		p.Style = pick(g, []string{"title_series", "title_bold", "title_lowercase", "label_box", "stat_big"})
	}
	if g.chance(30) {
		p.Energy = pick(g, []string{"calm", "lively", "hype"})
	}
	if g.chance(40) {
		n := 1 + g.r.IntN(4)
		for i := range n {
			p.AccentsMs = append(p.AccentsMs, at+int64(i+1)*dur/int64(n+2))
		}
		if g.chance(15) { // an accent past the item
			p.AccentsMs = append(p.AccentsMs, at+dur+100)
		}
	}
	switch template {
	case timeline.TemplateChapterCard:
		n := 1 + g.r.IntN(5)
		p.Number = &n
		if g.chance(30) {
			p.Layout = "two_line"
		}
	case timeline.TemplateHighlightWord:
		w := 1 + g.r.IntN(4)
		p.Word = &w
	case timeline.TemplateCounter:
		from, to := float64(g.r.IntN(10)), float64(10+g.r.IntN(200))
		p.From, p.To = &from, &to
		p.Text = "REPS"
	}
	return p
}

func (g *generator) addGraphic(k *known) Op {
	tpl := pick(g, timeline.GraphicsTemplates())
	o := &AddGraphic{Template: tpl, AtMs: int64(g.r.IntN(int(k.total) + 500)), DurationMs: int64(500 + g.r.IntN(3000))}
	o.ID = g.newID("gx", &k.graphics)
	if g.chance(75) {
		o.Anchor = pick(g, anchors)
	} else {
		o.Position = g.point()
	}
	o.Params = g.graphicsParams(tpl, o.AtMs, o.DurationMs)
	return o
}

func (g *generator) updateGraphic(k *known) Op {
	o := &UpdateGraphic{ID: g.id(k.graphics)}
	if g.chance(30) {
		v := int64(g.r.IntN(int(k.total) + 500))
		o.AtMs = &v
	}
	if g.chance(30) {
		v := int64(500 + g.r.IntN(2000))
		o.DurationMs = &v
	}
	switch g.r.IntN(4) {
	case 0:
		o.Anchor = pick(g, anchors)
	case 1:
		o.Position = g.point()
	}
	if g.chance(30) || (o.AtMs == nil && o.DurationMs == nil && o.Anchor == "" && o.Position == nil) {
		o.Template = pick(g, timeline.GraphicsTemplates())
		p := g.graphicsParams(o.Template, 0, 1000)
		o.Params = &p
	}
	return o
}

func TestGraphicsOps(t *testing.T) {
	cat := fixtureCatalog(t)
	base := parseSpec(t, []byte(`{"schema":"contentfabrique.timeline/v1","output":{"format":"9:16","width":1080,"height":1920,"fps":30,"background":"#000000"},
		"tracks":[{"id":"main","type":"video","items":[{"id":"c1","asset_id":"`+vid60+`","source":{"in_ms":0,"out_ms":8000},"at_ms":0}]}],"markers":[]}`))
	batch := `[{"op":"add_graphic","template":"title_pop","at_ms":0,"duration_ms":2400,"anchor":"upper_third","params":{"text":"BACK DAY 101","accents_ms":[484,968,1452]}},
		{"op":"add_graphic","template":"chapter_card","at_ms":3000,"duration_ms":2000,"anchor":"upper_third","params":{"text":"SEATED ROW","number":1}}]`
	ops := batchOf(t, batch)
	spec, res := Apply(base, ops, cat)
	if !res.OK() {
		t.Fatalf("%+v", res.Issues)
	}
	if len(res.Created) != 2 || res.Created[0].ID != "g1" || res.Created[1].ID != "g2" || res.Created[0].Kind != kindGraphic {
		t.Fatalf("created %+v", res.Created)
	}
	g := graphicsOf(t, spec)
	if g.ID != "graphics" || len(g.Items) != 2 {
		t.Fatalf("track %+v", g)
	}
	// Moving an item moves its accents with it.
	spec2, res := Apply(spec, batchOf(t, `[{"op":"update_graphic","id":"g1","at_ms":1000}]`), cat)
	if !res.OK() {
		t.Fatalf("%+v", res.Issues)
	}
	if got := graphicsOf(t, spec2).Items[0].Params.AccentsMs; got[0] != 1484 || got[2] != 2452 {
		t.Fatalf("accents %v did not move with the item", got)
	}
	if graphicsOf(t, spec).Items[0].Params.AccentsMs[0] != 484 {
		t.Fatal("the update changed the parent spec")
	}
	// params replace all of them, with the template.
	spec3, res := Apply(spec2, batchOf(t, `[{"op":"update_graphic","id":"g1","template":"counter","params":{"text":"REPS","from":0,"to":12}}]`), cat)
	if !res.OK() {
		t.Fatalf("%+v", res.Issues)
	}
	if it := graphicsOf(t, spec3).Items[0]; it.Template != timeline.TemplateCounter || it.Params.AccentsMs != nil || *it.Params.To != 12 {
		t.Fatalf("item %+v", it)
	}
	// An accent the item does not cover is refused, with the op blamed.
	_, res = Apply(spec, batchOf(t, `[{"op":"update_graphic","id":"g2","duration_ms":500,"template":"chapter_card","params":{"text":"SEATED ROW","number":1,"accents_ms":[3900]}}]`), cat)
	if res.OK() || res.Issues[0].Code != timeline.CodeInvalidGraphic || res.Issues[0].OpIndex != 0 {
		t.Fatalf("issues %+v", res.Issues)
	}
	// Removing, and the wrong kind of id.
	spec4, res := Apply(spec, batchOf(t, `[{"op":"remove_graphic","id":"g1"}]`), cat)
	if !res.OK() || len(graphicsOf(t, spec4).Items) != 1 {
		t.Fatalf("%+v", res.Issues)
	}
	_, res = Apply(spec, batchOf(t, `[{"op":"remove_graphic","id":"c1"}]`), cat)
	if res.OK() || res.Issues[0].Code != CodeWrongItemKind {
		t.Fatalf("issues %+v", res.Issues)
	}
	_, res = Apply(spec, batchOf(t, `[{"op":"update_text","id":"g1","text":"x"}]`), cat)
	if res.OK() || res.Issues[0].Code != CodeWrongItemKind {
		t.Fatalf("issues %+v", res.Issues)
	}
}

func graphicsOf(t *testing.T, spec timeline.Spec) *timeline.GraphicsTrack {
	t.Helper()
	for _, tr := range spec.Tracks {
		if g, ok := tr.(*timeline.GraphicsTrack); ok {
			return g
		}
	}
	t.Fatal("no graphics track")
	return nil
}
