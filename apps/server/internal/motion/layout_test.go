package motion

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/subject"
	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// The layout goldens: small specs where overlays meet, and where Layout puts each item (its time, its box, how it was kept off the
// person and off the other overlays). testdata/layout-golden.json is rewritten with -update; a change to it is a change to what the
// renderer draws, so review it.

const layoutAsset = "ast_01j3er3kwcgrasdgd4aznmxxe0"

type layoutCase struct {
	name     string
	totalMs  int64
	texts    string // text items, JSON
	graphics string // graphics items, JSON
	captions bool
	person   *subject.Track // the person in the clip, the whole time
}

// personBelow is a person whose head is at headTop (a fraction of the frame) and who fills the frame below it.
func personBelow(headTop float64) *subject.Track {
	s := subject.Sample{X: 0.08, Y: headTop - 0.01, W: 0.84, H: 1.01 - headTop, HeadTopY: headTop}
	a, b := s, s
	b.AtMs = 60000
	return &subject.Track{Aspect: 0.5625, Samples: []subject.Sample{a, b}}
}

func layoutCases() []layoutCase {
	return []layoutCase{
		{
			// The demo's bug: a counter on screen with a chapter card, both in the top band ("3. M12 REPS ROW"). The card is a title, so it
			// keeps its place; the counter is stacked below it.
			name: "counter_meets_chapter_card", totalMs: 30000,
			graphics: `{"id":"g4","template":"chapter_card","at_ms":19400,"duration_ms":2903,"anchor":"upper_third","params":{"text":"MACHINE ROW","number":3}},
				{"id":"g5","template":"counter","at_ms":20000,"duration_ms":2500,"anchor":"upper_third","params":{"text":"REPS","from":0,"to":12}}`,
		},
		{
			// A title_pop on screen with a text title in the same place: the text is drawn by the ASS script, the graphic makes way.
			name: "graphic_meets_text", totalMs: 10000,
			texts:    `{"id":"t1","text":"BACK DAY","style":"title_bold","anchor":"upper_third","at_ms":0,"duration_ms":3000}`,
			graphics: `{"id":"g1","template":"title_pop","at_ms":500,"duration_ms":2500,"anchor":"upper_third","params":{"text":"101"}}`,
		},
		{
			// Apart in time: nothing moves.
			name: "apart_in_time", totalMs: 10000,
			graphics: `{"id":"g1","template":"chapter_card","at_ms":0,"duration_ms":2500,"anchor":"upper_third","params":{"text":"SEATED ROW","number":1}},
				{"id":"g2","template":"counter","at_ms":2500,"duration_ms":2500,"anchor":"upper_third","params":{"text":"REPS","from":0,"to":12}}`,
		},
		{
			// A person whose head is high leaves room for one overlay above it: the counter cannot stack, so it is shown just after the card.
			name: "no_room_to_stack_moves_in_time", totalMs: 10000, person: personBelow(0.24),
			graphics: `{"id":"g1","template":"chapter_card","at_ms":1000,"duration_ms":2500,"anchor":"upper_third","params":{"text":"SEATED ROW","number":1}},
				{"id":"g2","template":"counter","at_ms":1500,"duration_ms":2500,"anchor":"upper_third","params":{"text":"REPS","from":0,"to":12}}`,
		},
		{
			// The same, with the card filling the video: no time is free, so the counter is left out.
			name: "no_room_anywhere_left_out", totalMs: 4000, person: personBelow(0.24),
			graphics: `{"id":"g1","template":"chapter_card","at_ms":0,"duration_ms":4000,"anchor":"upper_third","params":{"text":"SEATED ROW","number":1}},
				{"id":"g2","template":"counter","at_ms":500,"duration_ms":2500,"anchor":"upper_third","params":{"text":"REPS","from":0,"to":12}}`,
		},
		{
			// Two labels in the lower third with captions on: the second cannot go below the first (the bottom of the safe zone), so it goes
			// above it, and stops short of the caption band.
			name: "captions_band_stacks_above", totalMs: 10000, captions: true,
			graphics: `{"id":"g1","template":"lower_label","at_ms":0,"duration_ms":3000,"anchor":"lower_third","params":{"text":"TEMPO 3-1-1"}},
				{"id":"g2","template":"lower_label","at_ms":1000,"duration_ms":3000,"anchor":"lower_third","params":{"text":"SLOW NEGATIVE"}}`,
		},
	}
}

func (lc layoutCase) spec(t *testing.T) *timeline.Spec {
	t.Helper()
	tracks := []string{fmt.Sprintf(`{"id":"main","type":"video","items":[{"id":"c1","asset_id":%q,"source":{"in_ms":0,"out_ms":%d},"at_ms":0}]}`, layoutAsset, lc.totalMs)}
	if lc.texts != "" {
		tracks = append(tracks, `{"id":"text","type":"text","items":[`+lc.texts+`]}`)
	}
	if lc.captions {
		tracks = append(tracks, `{"id":"captions","type":"captions","enabled":true,"style":"chunk"}`)
	}
	tracks = append(tracks, `{"id":"graphics","type":"graphics","items":[`+lc.graphics+`]}`)
	js := `{"schema":"contentfabrique.timeline/v1","output":{"format":"9:16","width":1080,"height":1920,"fps":30,"background":"#000000"},"tracks":[` +
		strings.Join(tracks, ",") + `],"markers":[]}`
	spec, err := timeline.Parse([]byte(js))
	if err != nil {
		t.Fatalf("%s: %v", lc.name, err)
	}
	return spec
}

// laidSummary is what the golden file holds of a laid-out item.
type laidSummary struct {
	ID      string  `json:"id"`
	Drawn   bool    `json:"drawn"`
	Fit     string  `json:"fit"`
	Clash   string  `json:"clash,omitempty"`
	With    string  `json:"with,omitempty"`
	AtMs    int64   `json:"at_ms"`
	DurMs   int64   `json:"duration_ms"`
	Box     *[4]int `json:"box,omitempty"`
	Overlap string  `json:"overlap,omitempty"`
}

func TestLayoutGolden(t *testing.T) {
	got := map[string][]laidSummary{}
	for _, lc := range layoutCases() {
		spec := lc.spec(t)
		var lookup subject.Lookup
		if lc.person != nil {
			lookup = subject.Index{layoutAsset: *lc.person}.Lookup
		}
		laid, err := Layout(spec, lookup, lc.totalMs)
		if err != nil {
			t.Fatalf("%s: %v", lc.name, err)
		}
		texts, err := textObstacles(spec, lookup, lc.totalMs)
		if err != nil {
			t.Fatal(err)
		}
		var rows []laidSummary
		for i, l := range laid {
			r := laidSummary{ID: l.Original.ID, Drawn: l.Drawn(), Fit: l.Fit.How, Clash: l.Clash, With: l.With, AtMs: l.Item.AtMs, DurMs: l.Item.DurationMs}
			if l.Drawn() {
				b := l.Resolved.Box
				r.Box = &[4]int{b.Left, b.Top, b.Right, b.Bottom}
				// The invariant: no drawn overlay overlaps another drawn overlay or a text on screen at the same time.
				others := texts
				for j, m := range laid {
					if j != i && m.Drawn() {
						others = append(others, obstacle{id: m.Original.ID, start: m.Item.AtMs, end: m.Item.EndMs(), box: m.Resolved.Box})
					}
				}
				for _, o := range others {
					if o.start < l.Item.EndMs() && l.Item.AtMs < o.end && o.box.Overlaps(b) {
						r.Overlap = o.id
						t.Errorf("%s: %s is drawn over %s", lc.name, l.Original.ID, o.id)
					}
				}
			}
			rows = append(rows, r)
		}
		got[lc.name] = rows
	}
	data, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	const path = "testdata/layout-golden.json"
	if *update {
		if err := os.WriteFile(path, data, 0o644); err != nil { //nolint:gosec // a test golden
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update to write it)", err)
	}
	if !bytes.Equal(want, data) {
		t.Fatalf("the layout differs from %s (run with -update and review the diff):\n%s", path, data)
	}
}
