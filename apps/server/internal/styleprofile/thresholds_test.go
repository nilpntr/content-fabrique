package styleprofile

import (
	"fmt"
	"strings"
	"testing"
)

// montageRefs are n references of a fast creator: 13 shots of about a second, a first cut at 1 s, 8 effects a minute.
func montageRefs(t *testing.T, n int) []Measures {
	t.Helper()
	base := referenceMeasures(t)
	var out []Measures
	for i := 0; i < n; i++ {
		m := base[i%len(base)]
		m.Name = fmt.Sprintf("montage-%d.mp4", i)
		m.DurationS = 13
		m.CutsS = []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}
		m.ShotsS = []float64{1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1}
		m.Video.EffectsPerMinute = 9
		m.Text = nil
		out = append(out, m)
	}
	return out
}

// One odd reference moves a bound by a rank and never opens it: a montage creator with one 120 s single take and one video whose first
// cut comes at 30 s keeps tight thresholds.
func TestAnOutlierReferenceDoesNotOpenTheGates(t *testing.T) {
	clean := montageRefs(t, 5)
	cleanTh := Aggregate(clean).Thresholds()

	odd := montageRefs(t, 5)
	odd[3].DurationS, odd[3].CutsS, odd[3].ShotsS = 120, nil, []float64{120} // one take of two minutes
	odd[4].CutsS = []float64{30, 31, 32, 33, 34, 35, 36, 37, 38, 39, 40, 41} // a first cut at 30 s
	odd[4].ShotsS = []float64{30, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1}
	odd[4].DurationS = 42
	p := Aggregate(odd)
	th := p.Thresholds()

	if th.HookMaxS > HookCapS || th.FirstCutHiS > FirstCutCapS {
		t.Errorf("the hook is open: max %.1f s, window to %.1f s", th.HookMaxS, th.FirstCutHiS)
	}
	// The no-cut reference is no shot-length sample.
	if p.Pacing.ShotMedianS.Max > 30 || p.Pacing.ShotMedianS.Median != 1 {
		t.Errorf("shot medians: %+v", p.Pacing.ShotMedianS)
	}
	if th.MedianShotMaxS > 2.5*p.Pacing.ShotMedianS.Median+0.001 || th.MedianShotMaxS > 5 {
		t.Errorf("median shot limit %.1f s", th.MedianShotMaxS)
	}
	if th.DurHiS > 2.5*p.Format.DurationS.Median+0.001 {
		t.Errorf("duration limit %.1f s", th.DurHiS)
	}
	if th.CutsHi > 3*p.Pacing.CutsPerVideo.Median+3 {
		t.Errorf("cuts limit %.1f", th.CutsHi)
	}
	// And it is close to the clean creator's.
	if th.MedianShotMaxS > cleanTh.MedianShotMaxS*2 || th.HookMaxS > cleanTh.HookMaxS+1.5 {
		t.Errorf("clean %+v against odd %+v", cleanTh, th)
	}
	// A slow edit (a 10 s median shot and an 8 s opening) is out of this creator's range.
	d := p.Compare(EditStats{DurationS: 40, ShotsS: []float64{8, 10, 10, 12}, FirstCutS: 8, Effects: 2})
	bad := map[string]bool{}
	for _, x := range d.Failed() {
		bad[x.ID] = true
	}
	for _, id := range []string{"cuts", "first_cut", "shot_length"} {
		if !bad[id] {
			t.Errorf("%s is not out of range:\n%s", id, d.Text())
		}
	}
}

// The thresholds of the five Gino references, for the record and so that the long-take style stays explicit.
func TestThresholdsOfALongTakeCreator(t *testing.T) {
	p := Aggregate(referenceMeasures(t))
	th := p.Thresholds()
	t.Logf("thresholds: %+v", th)
	if p.Pacing.Style != PaceLongTake || th.LongTakeNeedS < 4 || th.LongTakeNeedS > 8 {
		t.Errorf("long take need %.1f s (%s)", th.LongTakeNeedS, p.Pacing.Style)
	}
	if th.HookMaxS < 2.4 || th.HookMaxS > 3.2 || th.FirstCutHiS > 4 {
		t.Errorf("hook %.1f s, first cut to %.1f s", th.HookMaxS, th.FirstCutHiS)
	}
	// The shot-length statistics are those of the three references that have a cut, so the no-cut 43.8 s and 28.6 s videos are out.
	if p.Pacing.ShotMedianS.Max > 4 {
		t.Errorf("shot medians %+v", p.Pacing.ShotMedianS)
	}
	// The explicit long-take stat bounds the median shot.
	if th.MedianShotMaxS != p.Pacing.LongestShotS.Upper() {
		t.Errorf("median shot limit %.1f against the longest-take quartile %.1f", th.MedianShotMaxS, p.Pacing.LongestShotS.Upper())
	}
}

func TestCutsFixFollowsTheBoundThatFailed(t *testing.T) {
	p := Aggregate(montageRefs(t, 5))
	th := p.Thresholds()
	fix := func(cuts int, durS float64) string {
		shots := make([]float64, cuts+1)
		for i := range shots {
			shots[i] = durS / float64(cuts+1)
		}
		d := p.Compare(EditStats{DurationS: durS, ShotsS: shots, FirstCutS: 1})
		for _, x := range d.Dimensions {
			if x.ID == "cuts" {
				return x.Fix
			}
		}
		return "?"
	}
	if f := fix(int(th.CutsHi)+10, 30); !strings.Contains(f, "fewer cuts") || strings.Contains(f, "add cuts") {
		t.Errorf("too many cuts: %q", f)
	}
	if f := fix(1, 30); !strings.Contains(f, "add cuts") {
		t.Errorf("too few cuts: %q", f)
	}
	// In count but too fast: 11 cuts in 3 s.
	if f := fix(11, 3); !strings.Contains(f, "fewer cuts or longer takes") {
		t.Errorf("too fast: %q", f)
	}
	if f := fix(12, 13); f != "" {
		t.Errorf("an in-range edit has a fix: %q", f)
	}
}

func TestQuartiles(t *testing.T) {
	r := RangeOf([]float64{1, 2, 3, 4, 100})
	if r.P25 != 2 || r.P75 != 4 || r.Max != 100 || r.Upper() != 4 || r.Lower() != 2 {
		t.Errorf("%+v", r)
	}
	// Zero quartiles are real: one odd value among zeros does not open the bound.
	if z := RangeOf([]float64{0, 0, 0, 0, 10}); z.Upper() != 0 || z.Max != 10 {
		t.Errorf("zeros and one outlier: %+v", z)
	}
	ms := montageRefs(t, 5)
	for i := range ms {
		ms[i].Video.EffectsPerMinute = 0
	}
	ms[4].Video.EffectsPerMinute = 7
	p := Aggregate(ms)
	if p.Effects.Verdict != EffectsNone || p.Thresholds().FxPerMinHi > 3.001 {
		t.Errorf("one outlier reference of effects: verdict %s, limit %.1f", p.Effects.Verdict, p.Thresholds().FxPerMinHi)
	}
	// A creator whose first cuts all come late does not get an empty window.
	late := montageRefs(t, 5)
	for i := range late {
		late[i].CutsS = []float64{6, 7, 8}
		late[i].ShotsS = []float64{6, 1, 1, 5}
	}
	if th := Aggregate(late).Thresholds(); th.FirstCutLoS >= th.FirstCutHiS {
		t.Errorf("first cut window %.1f to %.1f", th.FirstCutLoS, th.FirstCutHiS)
	}
	if (Range{}).Upper() != 0 || RangeOf(nil) != (Range{}) {
		t.Error("empty")
	}
}
