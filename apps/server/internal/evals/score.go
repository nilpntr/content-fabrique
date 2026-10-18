package evals

import (
	"fmt"
	"math"
	"sort"

	"github.com/nilpntr/content-fabrique/internal/agents/editor"
)

// The score of a case is a weighted mean, from 0 to 100, of parts that are each from 0 to 1:
//
//	checks       weight 2   the share of the applicable check_edit checks that pass
//	style        weight 1   the style distance's score (the share of the creator's dimensions in range), when there is a profile
//	judge        weight 1   each of the six judged dimensions, (score-1)/4; ai_look inverted
//	music_sync   weight 1   measured: the share of cuts on a beat, as a 1-to-5 score
//	rubric       its weight each of the case's rubric items, as the judge scored it
//	feedback     its weight each measured item of recorded human feedback, pass 1 or fail 0 (default weight 3: it outweighs the judge)
//
// A part that does not apply (no judge, no profile, a skipped item) is left out and the rest are renormalised.

// The weights of the parts that have no weight of their own.
const (
	WeightChecks    = 2
	WeightStyle     = 1
	WeightDimension = 1
)

// Part is one term of a case's score.
type Part struct {
	Name   string  `json:"name"`
	Value  float64 `json:"value"`
	Weight float64 `json:"weight"`
}

// MusicSync scores the music sync from the measured beat data of the check report: the share of the cuts within the beat tolerance.
// ok is false when the edit has no beat grid or no cuts.
func MusicSync(r editor.Report) (Score, bool) {
	m := r.Metrics
	if m.Cuts == 0 {
		return Score{}, false
	}
	applies := false
	for _, c := range r.Checks {
		if c.ID == editor.CheckBeatSync && c.Status != editor.CheckSkip {
			applies = true
		}
	}
	if !applies {
		return Score{}, false
	}
	share := float64(m.CutsOnBeat) / float64(m.Cuts)
	s := 1
	switch {
	case share >= 0.85:
		s = 5
	case share >= 0.7:
		s = 4
	case share >= 0.5:
		s = 3
	case share >= 0.3:
		s = 2
	}
	return Score{Score: s, Rationale: fmt.Sprintf("measured: %d of %d cuts within the beat tolerance (%.0f %%)", m.CutsOnBeat, m.Cuts, share*100)}, true
}

// norm maps a 1-to-5 score to 0..1; ai_look is inverted (5 is an obvious AI edit).
func norm(dim string, s int) float64 {
	v := float64(s-1) / 4
	if dim == DimAILook {
		v = 1 - v
	}
	return v
}

// Compose builds a case's parts and its score (0 to 100; NaN when no part applies).
func Compose(r editor.Report, verdict *Verdict, music *Score, rubric []Expectation, feedback []FeedbackResult) ([]Part, float64) {
	var parts []Part
	pass, fail, _ := r.Counts()
	if pass+fail > 0 {
		parts = append(parts, Part{Name: "checks", Value: float64(pass) / float64(pass+fail), Weight: WeightChecks})
	}
	if r.Style != nil && len(r.Style.Dimensions) > 0 {
		parts = append(parts, Part{Name: "style", Value: r.Style.Score, Weight: WeightStyle})
	}
	if verdict != nil && len(verdict.Scores) > 0 {
		dims := make([]string, 0, len(verdict.Scores))
		for d := range verdict.Scores {
			dims = append(dims, d)
		}
		sort.Strings(dims)
		for _, d := range dims {
			parts = append(parts, Part{Name: "judge." + d, Value: norm(d, verdict.Scores[d].Score), Weight: WeightDimension})
		}
		for _, e := range rubric {
			if s, ok := verdict.Expectations[e.ID]; ok && e.Weight > 0 {
				parts = append(parts, Part{Name: "rubric." + e.ID, Value: norm("", s.Score), Weight: e.Weight})
			}
		}
	}
	if music != nil {
		parts = append(parts, Part{Name: "music_sync", Value: norm(DimMusicSync, music.Score), Weight: WeightDimension})
	}
	for _, f := range feedback {
		switch f.Status {
		case Pass:
			parts = append(parts, Part{Name: "feedback." + f.ID, Value: 1, Weight: f.Weight})
		case Fail:
			parts = append(parts, Part{Name: "feedback." + f.ID, Value: 0, Weight: f.Weight})
		}
	}
	var sum, weights float64
	for _, p := range parts {
		sum += p.Value * p.Weight
		weights += p.Weight
	}
	if weights == 0 {
		return parts, math.NaN()
	}
	return parts, math.Round(1000*sum/weights) / 10
}

// CalibrationResult says whether the judge agreed with a recorded human verdict.
type CalibrationResult struct {
	Note   string   `json:"note"`
	Agrees bool     `json:"agrees"`
	Off    []string `json:"off,omitempty"`
}

// Calibrate compares a verdict with a calibration case's expected ranges.
func Calibrate(c *Calibration, v *Verdict, music *Score) *CalibrationResult {
	if c == nil {
		return nil
	}
	res := &CalibrationResult{Note: c.Note, Agrees: true}
	if v == nil {
		return &CalibrationResult{Note: c.Note, Off: []string{"the judge did not run, so there is nothing to compare"}}
	}
	dims := make([]string, 0, len(c.Expect))
	for d := range c.Expect {
		dims = append(dims, d)
	}
	sort.Strings(dims)
	for _, d := range dims {
		b := c.Expect[d]
		var s Score
		var ok bool
		if d == DimMusicSync {
			if music != nil {
				s, ok = *music, true
			}
		} else {
			s, ok = v.Scores[d]
		}
		if !ok {
			res.Off = append(res.Off, d+": not scored")
			res.Agrees = false
			continue
		}
		if s.Score < b[0] || s.Score > b[1] {
			res.Off = append(res.Off, fmt.Sprintf("%s: %d, the human verdict says %d to %d", d, s.Score, b[0], b[1]))
			res.Agrees = false
		}
	}
	return res
}
