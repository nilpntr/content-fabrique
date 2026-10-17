package styleprofile

import (
	"strings"
	"testing"
)

// "Building Lats 101", the creator's own edit: a 2 s pose with the title, 1.3 s of the straps going on, a half second of walking in, an
// 8 s row and a 3 s front row, with no effects, no look, no sound effects and no labels.
func buildingLats() EditStats {
	return EditStats{
		DurationS: 14.8, ShotsS: []float64{2, 1.3, 0.5, 8, 3}, FirstCutS: 2,
		Titles: 1, TitleCase: "upper", TitleAtS: 0,
	}
}

func TestAnEditInTheCreatorsStylePasses(t *testing.T) {
	p := Aggregate(referenceMeasures(t))
	d := p.Compare(buildingLats())
	if len(d.Failed()) != 0 || d.Score != 1 {
		t.Fatalf("the creator's own structure is out of range:\n%s", d.Text())
	}
	// The dimensions we measure are all there.
	ids := map[string]bool{}
	for _, x := range d.Dimensions {
		ids[x.ID] = true
	}
	for _, want := range []string{"cuts", "long_takes", "first_cut", "effects", "title", "labels", "title_case", "captions", "grade", "sfx", "duration"} {
		if !ids[want] {
			t.Errorf("no %s dimension: %v", want, ids)
		}
	}
}

// What a generic fast workout edit does to the same footage, each thing a fix names.
func TestAGenericEditIsOutOfRangeWithFixes(t *testing.T) {
	p := Aggregate(referenceMeasures(t))
	generic := EditStats{
		DurationS: 28, ShotsS: []float64{1.5, 1.5, 3, 3, 1, 1, 3, 3, 1.5, 3, 3, 3.5, 1, 1.5}, FirstCutS: 0.8,
		Effects: 6, Titles: 1, Labels: 3, TitleCase: "mixed", Graded: true, SFX: 4, Captions: true,
	}
	d := p.Compare(generic)
	bad := map[string]Dimension{}
	for _, x := range d.Failed() {
		bad[x.ID] = x
	}
	for _, id := range []string{"cuts", "long_takes", "first_cut", "effects", "labels", "title_case", "captions", "grade", "sfx"} {
		x, ok := bad[id]
		if !ok || x.Fix == "" || strings.Contains(x.Fix, "\n") {
			t.Errorf("%s is not out of range with a fix: %+v", id, x)
		}
	}
	if d.Score > 0.4 {
		t.Errorf("score %.2f", d.Score)
	}
	if !strings.Contains(bad["effects"].Fix, "none") || !strings.Contains(bad["long_takes"].Fix, "keep one take running") {
		t.Errorf("fixes: %q / %q", bad["effects"].Fix, bad["long_takes"].Fix)
	}
	text := d.Text()
	if !strings.Contains(text, "OUT  effects") || !strings.Contains(text, "ok   duration") {
		t.Errorf("text:\n%s", text)
	}
}

func TestCaseOf(t *testing.T) {
	for text, want := range map[string]string{"BUILDING LATS 101": "upper", "how to bake a croissant tricep": "lower", "Heavy Lat Day": "mixed", "101": "mixed", "ÉTÉ": "upper"} {
		if got := CaseOf(text); got != want {
			t.Errorf("%q: %s, want %s", text, got, want)
		}
	}
}

// A profile of a fast creator does not ask for long takes, and one with effects allows them.
func TestAFastCreatorsProfile(t *testing.T) {
	ms := referenceMeasures(t)
	for i := range ms {
		ms[i].CutsS = []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}
		ms[i].ShotsS = []float64{1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1}
		ms[i].DurationS = 13
		ms[i].Video.EffectsPerMinute = 10
	}
	p := Aggregate(ms)
	if p.Pacing.Style != PaceMontage || p.Effects.Verdict != EffectsBusy || p.Pacing.HasLongTakes {
		t.Fatalf("pacing %s, effects %s", p.Pacing.Style, p.Effects.Verdict)
	}
	d := p.Compare(EditStats{DurationS: 14, ShotsS: []float64{1, 1, 1, 1, 1, 1, 1, 1}, FirstCutS: 1, Effects: 2, Titles: 1})
	for _, x := range d.Dimensions {
		if x.ID == "long_takes" {
			t.Error("a fast creator was asked for long takes")
		}
		if x.ID == "effects" && !x.OK {
			t.Errorf("effects: %+v", x)
		}
	}
}
