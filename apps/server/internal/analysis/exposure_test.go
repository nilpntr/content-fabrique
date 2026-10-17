package analysis

import (
	"strings"
	"testing"
)

func flat(n int, luma, high float64) Series {
	s := Series{StepMs: 100, Luma: make([]float64, n), High: make([]float64, n)}
	for i := range n {
		s.Luma[i], s.High[i] = luma, high
	}
	return s
}

func TestExposureOfStretches(t *testing.T) {
	// 5 s of a dark gym (p90 luma 0.1), then 5 s of a lit wall (p90 0.8), 8-bit limited range.
	s := flat(100, 24, 38)
	for i := 50; i < 100; i++ {
		s.Luma[i], s.High[i] = 100, 191
	}
	first, ok := s.ExposureBetween(0, 5000)
	if !ok || first.P90 != 0.1 || first.Mean != 0.04 {
		t.Errorf("first half: %+v %v", first, ok)
	}
	second, _ := s.ExposureBetween(5000, 10000)
	if second.P90 != 0.8 {
		t.Errorf("second half: %+v", second)
	}
	// A median, so a single bright frame does not move a shot.
	s.High[10] = 255
	if e, _ := s.ExposureBetween(0, 5000); e.P90 != 0.1 {
		t.Errorf("one bright frame moved the p90 to %v", e.P90)
	}
	if j := Jump(first, second); j != 0.7 {
		t.Errorf("jump %v", j)
	}
	// Edges: a stretch inside one sample, past the end, and an empty series.
	if _, ok := s.ExposureBetween(120, 130); !ok {
		t.Error("a stretch shorter than a sample has no exposure")
	}
	if _, ok := s.ExposureBetween(20000, 21000); ok {
		t.Error("a stretch past the end has an exposure")
	}
	if _, ok := (Series{StepMs: 100}).WholeExposure(); ok {
		t.Error("an empty series has an exposure")
	}
	if e, ok := s.WholeExposure(); !ok || e.P90 < 0.1 {
		t.Errorf("whole: %+v %v", e, ok)
	}
}

// Low-key footage is not underexposed: the founder's gym clips (mean 0.00 to 0.07, p90 0.10 to 0.33) all render correctly.
func TestExposureLevels(t *testing.T) {
	cases := []struct {
		e    Exposure
		want string
		note string
	}{
		{Exposure{P90: 0.33, Mean: 0.07}, ExposureLowKey, "low-key (p90 luma 0.33)"},
		{Exposure{P90: 0.18, Mean: 0.04}, ExposureLowKey, "low-key (p90 luma 0.18)"},
		{Exposure{P90: 0.10, Mean: 0.00}, ExposureLowKey, "low-key (p90 luma 0.10)"},
		{Exposure{P90: 0.03, Mean: 0.00}, ExposureUnderexposed, "underexposed (p90 luma 0.03)"},
		{Exposure{P90: 0.75, Mean: 0.45}, ExposureOK, ""},
		{Exposure{P90: 0.97, Mean: 0.80}, ExposureBright, "bright (p90 luma 0.97)"},
		{Exposure{P90: 0.97, Mean: 0.30}, ExposureOK, ""}, // a bright window in a normal room
	}
	for _, c := range cases {
		if got := c.e.Level(); got != c.want {
			t.Errorf("%+v: level %s, want %s", c.e, got, c.want)
		}
		if got := c.e.Note(); got != c.note {
			t.Errorf("%+v: note %q, want %q", c.e, got, c.note)
		}
	}
}

// The summary line used to say "dark (brightness 10%, a gain of x2.5 would be needed)" about footage that renders correctly. It must
// never suggest a gain, with or without the percentiles.
func TestSummaryLineNeverSuggestsAGain(t *testing.T) {
	sum := Summary{Reps: 8, Sets: 1, PeriodMs: 3000, DeadMs: 12000, Level: "dark", MeanY: 0.1, Gain: 2.5}
	line := sum.Line()
	if strings.Contains(line, "gain") || strings.Contains(line, "x2.5") || !strings.Contains(line, "dark overall (key brightness 10%; may be low-key footage, which is fine)") {
		t.Errorf("without percentiles: %s", line)
	}
	line = sum.WithSeries(flat(100, 24, 56)).Line()
	if strings.Contains(line, "gain") || !strings.Contains(line, "low-key (p90 luma 0.18)") || strings.Contains(line, "dark") {
		t.Errorf("with percentiles: %s", line)
	}
	line = sum.WithSeries(flat(100, 17, 20)).Line()
	if !strings.Contains(line, "underexposed (p90 luma 0.02)") {
		t.Errorf("underexposed: %s", line)
	}
	// A well-lit clip says nothing about its exposure.
	ok := Summary{Reps: 8, Sets: 1, Level: "ok", MeanY: 0.4}.WithSeries(flat(100, 120, 180)).Line()
	if strings.Contains(ok, "luma") || strings.Contains(ok, "dark") || strings.Contains(ok, "bright") {
		t.Errorf("a well-lit clip: %s", ok)
	}
}

// The analysis fills in the percentiles of a clip it has just analysed, from the same series the store keeps, so the line printed
// when the job ends equals the line the editor reads later.
func TestStoredSeriesGiveTheSameExposure(t *testing.T) {
	s := flat(80, 24, 56)
	packed, err := Pack(s)
	if err != nil {
		t.Fatal(err)
	}
	back := ExposureSeries(s.StepMs, packed.Luma, packed.High)
	a, _ := s.WholeExposure()
	b, ok := back.WholeExposure()
	if !ok || a != b {
		t.Errorf("exposure of the stored series %+v, of the computed %+v", b, a)
	}
}
