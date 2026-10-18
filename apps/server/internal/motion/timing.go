package motion

import (
	"math"
	"strings"

	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// Feel is the motion of one energy: how long an element takes to enter, how far apart elements enter, how much a word overshoots,
// how much it is blurred on the way in, how far it drifts, how much it floats while it holds, and how long the exit takes. Sizes
// are pixels of the 1080-wide layout (every format's short side is 1080).
type Feel struct {
	DurMs     int64   `json:"dur_ms"`
	StaggerMs int64   `json:"-"`
	Overshoot float64 `json:"overshoot"`
	BlurPx    float64 `json:"blur_px"`
	DriftPx   float64 `json:"drift_px"`
	FloatPx   float64 `json:"float_px"`
	ExitMs    int64   `json:"-"`
}

// Feels are the three energies. The staggers are in the 0.08 to 0.35 s range of word-by-word reveals (MOTION_PHILOSOPHY.md of
// herk-edit-kit: something new every 0.3 to 0.6 s, words 0.3 to 0.4 s apart for a narrative, faster for a punch).
var Feels = map[string]Feel{
	timeline.EnergyCalm:   {DurMs: 560, StaggerMs: 300, Overshoot: 1.03, BlurPx: 8, DriftPx: 18, FloatPx: 0, ExitMs: 420},
	timeline.EnergyLively: {DurMs: 440, StaggerMs: 170, Overshoot: 1.07, BlurPx: 12, DriftPx: 24, FloatPx: 3, ExitMs: 320},
	timeline.EnergyHype:   {DurMs: 340, StaggerMs: 90, Overshoot: 1.12, BlurPx: 16, DriftPx: 30, FloatPx: 4, ExitMs: 240},
}

// FeelOf is the feel of an item's energy.
func FeelOf(it timeline.GraphicsItem) Feel { return Feels[it.Params.EnergyOrDefault()] }

// Timing is when the parts of an item move, in ms from the item's start.
type Timing struct {
	// EnterMs are the entrance starts of the template's elements (words, lines, or the number and the line).
	EnterMs []int64 `json:"enter_ms"`
	// HitsMs are pulses after the entrance, on the accents the entrance did not use.
	HitsMs []int64 `json:"hits_ms"`
	// FullyInMs is when everything has entered: from here to ExitMs the item can be read.
	FullyInMs int64 `json:"fully_in_ms"`
	// ExitMs is when the exit starts and ExitDurMs how long it takes (it ends with the item).
	ExitMs    int64 `json:"exit_ms"`
	ExitDurMs int64 `json:"exit_dur_ms"`
	// SwipeMs is when highlight_word's marker starts; LandMs is when counter's count lands.
	SwipeMs int64 `json:"swipe_ms,omitempty"`
	LandMs  int64 `json:"land_ms,omitempty"`
	// LegibleMs is when the last of the text has landed and LeavingMs when the exit has visibly begun (readable.go): between the two
	// the item can be read.
	LegibleMs int64 `json:"legible_ms"`
	LeavingMs int64 `json:"leaving_ms"`
}

// ReadableMs is how long the item can be read: from when its text has landed until its exit has visibly begun.
func (t Timing) ReadableMs() int64 { return max(0, t.LeavingMs-t.LegibleMs) }

// Schedule computes an item's timing. lines is the number of lines its text takes once laid out (blur_in and highlight_word enter
// line by line); the accents are the item's accents_ms. An entrance that an accent would put before the item's start starts at 0.
func Schedule(it timeline.GraphicsItem, lines int) Timing {
	f := FeelOf(it)
	dur := it.DurationMs
	acc := make([]int64, 0, len(it.Params.AccentsMs))
	for _, a := range it.Params.AccentsMs {
		acc = append(acc, a-it.AtMs)
	}
	lines = max(1, lines)
	exitDur := min(f.ExitMs, dur/4)
	t := Timing{ExitMs: dur - exitDur, ExitDurMs: exitDur}
	// at places an entrance so that its peak (peak of its duration in) lands on accent i, or on the stagger after the previous.
	at := func(i int, prev int64, peak float64, d int64) int64 {
		if i < len(acc) {
			return max(prev, acc[i]-int64(math.Round(peak*float64(d))))
		}
		if i == 0 {
			return 0
		}
		return prev + f.StaggerMs
	}
	rest := func(from int) []int64 {
		if from >= len(acc) {
			return nil
		}
		return append([]int64(nil), acc[from:]...)
	}
	switch it.Template {
	case timeline.TemplateTitlePop:
		words := max(1, len(it.Words()))
		var prev int64
		for i := 0; i < words; i++ {
			s := at(i, prev, 0.6, f.DurMs)
			if i == 0 {
				s = max(0, s)
			}
			t.EnterMs = append(t.EnterMs, s)
			prev = s
		}
		t.FullyInMs = prev + f.DurMs
		t.HitsMs = rest(words)
	case timeline.TemplateBlurIn:
		d := int64(float64(f.DurMs) * 1.6)
		var prev int64
		for i := 0; i < lines; i++ {
			s := max(0, at(i, prev, 0.6, d))
			t.EnterMs = append(t.EnterMs, s)
			prev = s
		}
		t.FullyInMs = prev + d
		t.HitsMs = rest(lines)
	case timeline.TemplateChapterCard:
		num := max(0, at(0, 0, 0.55, f.DurMs))
		sweep := num + max(f.StaggerMs*3/2, f.DurMs*45/100)
		if len(acc) > 1 {
			sweep = max(num, acc[1]-f.DurMs*3/10)
		}
		t.EnterMs = []int64{num, sweep}
		restWords := max(1, len(it.Words())-1)
		step := max(25, min(70, f.StaggerMs/2))
		t.FullyInMs = max(num+f.DurMs, sweep+int64(restWords-1)*step+f.DurMs*11/10)
		t.HitsMs = rest(2)
	case timeline.TemplateHighlightWord:
		var prev int64
		for i := 0; i < lines; i++ {
			s := int64(i) * f.StaggerMs
			t.EnterMs = append(t.EnterMs, s)
			prev = s
		}
		textIn := prev + f.DurMs*12/10
		t.SwipeMs = textIn + 120
		if len(acc) > 0 {
			t.SwipeMs = max(0, acc[0]-f.DurMs/2)
		}
		t.FullyInMs = max(textIn, t.SwipeMs+f.DurMs)
		t.HitsMs = rest(1)
	case timeline.TemplateCounter:
		t.EnterMs = []int64{0, f.DurMs * 4 / 10}
		t.LandMs = min(1000, dur*45/100)
		if len(acc) > 0 {
			t.LandMs = max(f.DurMs/2, acc[0])
		}
		t.FullyInMs = max(t.LandMs+240, t.EnterMs[1]+f.DurMs)
		t.HitsMs = rest(1)
	case timeline.TemplateLowerLabel:
		s := max(0, at(0, 0, 0.25, f.DurMs))
		text := s + f.DurMs*6/10
		t.EnterMs = []int64{s, text}
		t.FullyInMs = text + int64(lines-1)*f.StaggerMs + f.DurMs
		t.HitsMs = rest(1)
	case timeline.TemplateOutroCard:
		s := max(0, at(0, 0, 0, f.DurMs))
		letters := int64(len([]rune(strings.Join(it.Words(), ""))))
		per := max(12, min(45, f.StaggerMs/3))
		t.EnterMs = []int64{s}
		for i := 1; i < lines; i++ {
			t.EnterMs = append(t.EnterMs, s)
		}
		t.FullyInMs = s + letters*per + f.DurMs
		t.HitsMs = rest(1)
	}
	t.readableWindow(it, f, lines)
	return t
}
