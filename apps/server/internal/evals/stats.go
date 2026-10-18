package evals

import (
	"fmt"
	"math"
)

// Statistics of a suite run (docs/design/observability-v1.md section 9). A live editor run is not deterministic: the same code on the same
// case scored 91.0, 85.0 and 83.7 in three runs. So a live case is run --repeat times, a case's score is the mean of its samples with its
// standard deviation, a failed sample (the run failed on budget, an answer cut off at max_tokens, an error) scores 0 and is listed, and a
// change is kept only when its gain stands out of that noise.

// SampleResult is one run of a case (a live case has --repeat of them, a stored one a single one).
type SampleResult struct {
	N     int      `json:"n"`
	Score *float64 `json:"score,omitempty"`
	// Failed says the sample is a bad outcome that scores 0 (the editor run failed, or made no version); Reason says why.
	Failed bool   `json:"failed,omitempty"`
	Reason string `json:"reason,omitempty"`
	// Unjudged says the judge was on but gave no verdict for this sample: its score lacks the judge's parts, so it is left out of the mean
	// when another sample of the case was judged (scores with and without the judge are not on one scale).
	Unjudged    bool   `json:"unjudged,omitempty"`
	EditorRun   string `json:"editor_run,omitempty"`
	Project     string `json:"project,omitempty"`
	Version     int    `json:"version,omitempty"`
	EditMicros  int64  `json:"edit_micros,omitempty"`
	JudgeMicros int64  `json:"judge_micros,omitempty"`
	TraceID     string `json:"trace_id,omitempty"`
}

// counted is the sample's value in the case's mean, and whether it counts: a failure counts as 0, an unscored sample not at all, and an
// unjudged one only when no sample of the case was judged (judgedAny false).
func (s SampleResult) counted(judgedAny bool) (float64, bool) {
	switch {
	case s.Failed:
		return 0, true
	case s.Score == nil, s.Unjudged && judgedAny:
		return 0, false
	}
	return *s.Score, true
}

// aggregate sets a case's score from its samples: the mean, the sample standard deviation (with two or more), the count and the failures.
func aggregate(cr *CaseResult) {
	var vals []float64
	cr.Failures = nil
	judgedAny := false
	for _, s := range cr.Samples {
		judgedAny = judgedAny || (!s.Failed && !s.Unjudged && s.Score != nil)
	}
	for _, s := range cr.Samples {
		if v, ok := s.counted(judgedAny); ok {
			vals = append(vals, v)
		}
		if s.Failed {
			cr.Failures = append(cr.Failures, fmt.Sprintf("sample %d: %s", s.N, s.Reason))
		}
	}
	cr.N = len(vals)
	cr.Score, cr.SD = nil, nil
	if cr.N == 0 {
		return
	}
	mean, sd := meanSD(vals)
	m := math.Round(10*mean) / 10
	cr.Score = &m
	if cr.N >= 2 {
		s := math.Round(10*sd) / 10
		cr.SD = &s
	}
	if len(cr.Samples) > 0 {
		cr.FailureRate = float64(len(cr.Failures)) / float64(len(cr.Samples))
	}
}

func meanSD(v []float64) (float64, float64) {
	var sum float64
	for _, x := range v {
		sum += x
	}
	mean := sum / float64(len(v))
	if len(v) < 2 {
		return mean, 0
	}
	var ss float64
	for _, x := range v {
		ss += (x - mean) * (x - mean)
	}
	return mean, math.Sqrt(ss / float64(len(v)-1))
}

// summarise sets a run's means, their standard errors and its failure rate.
func summarise(r *RunResult) {
	var main, held []CaseResult
	r.Samples, r.Failures = 0, 0
	for _, c := range r.Cases {
		r.Samples += len(c.Samples)
		r.Failures += len(c.Failures)
		if c.Score == nil || c.Calibration != nil {
			continue
		}
		if c.HeldOut {
			held = append(held, c)
		} else {
			main = append(main, c)
		}
	}
	r.Mean, r.MeanSE = meanOfCases(main, DefaultNoiseSD)
	r.HeldOut, r.HeldOutSE = meanOfCases(held, DefaultNoiseSD)
	if r.Samples > 0 {
		r.FailureRate = math.Round(1000*float64(r.Failures)/float64(r.Samples)) / 1000
	}
	var calErr, calN float64
	for _, c := range r.Cases {
		if c.Calibration != nil && c.Calibration.Judged {
			calErr, calN = calErr+c.Calibration.Error, calN+1
		}
	}
	if calN > 0 {
		v := math.Round(100*calErr/calN) / 100
		r.CalibrationError = &v
	}
}

// DefaultNoiseSD is the standard deviation assumed for a live case scored once: the spread the first live runs of the back-day suite
// showed (the same code and case at 91.0, 85.0 and 83.7; the held-out case at 89.3, 84.2 and 90.1).
const DefaultNoiseSD = 4.0

// caseVar is the variance of a case's mean: sd²/n with its own sd when it has two or more samples, the noise sd when it is a live case
// scored once (one sample says nothing about its spread), and 0 for a stored case (its checks are computed in Go and its judge verdict
// comes from the cache: the same version scores the same).
func caseVar(c CaseResult, noiseSD float64) float64 {
	switch {
	case c.SD != nil && c.N >= 2:
		return (*c.SD) * (*c.SD) / float64(c.N)
	case c.Mode == "live" && c.N >= 1:
		return noiseSD * noiseSD / float64(c.N)
	}
	return 0
}

// meanOfCases is the mean of the cases' means and its standard error (the square root of the summed variances over the count).
func meanOfCases(cases []CaseResult, noiseSD float64) (*float64, *float64) {
	if len(cases) == 0 {
		return nil, nil
	}
	var sum, v float64
	for _, c := range cases {
		sum += *c.Score
		v += caseVar(c, noiseSD)
	}
	k := float64(len(cases))
	m := math.Round(10*sum/k) / 10
	se := math.Round(10*math.Sqrt(v)/k) / 10
	return &m, &se
}

// Rule is the acceptance rule of a change.
type Rule struct {
	// MinGain is the least rise of the mean (points of 100), whatever the noise.
	MinGain float64
	// MaxRegression is how far one case's mean may drop, and HeldOutSlack the held-out mean, beyond the noise of that comparison.
	MaxRegression, HeldOutSlack float64
	// NoiseSD is the standard deviation of a live case scored once (DefaultNoiseSD when 0).
	NoiseSD float64
}

// Decision is the rule applied, with its numbers.
type Decision struct {
	Accepted bool     `json:"accepted"`
	Gain     float64  `json:"gain"`
	GainSE   float64  `json:"gain_se"`
	Cases    int      `json:"cases"`
	Reasons  []string `json:"reasons"`
}

// Accept compares a candidate with its baseline, case by case (a paired comparison: each case is its own control, so the spread between
// cases does not count as noise):
//
//   - the gain is the mean over the cases scored in both (not held out, not calibration) of mean_after - mean_before, and its standard
//     error is sqrt(sum over cases of var_before/n_before + var_after/n_after) / cases;
//   - (a) kept only if the gain is at least MinGain and more than twice its standard error (about a 95 % one-sided test under normal
//     noise; with two or three samples a case's sd is rough, which the factor 2 absorbs better than a t-table would pretend to);
//   - (b) no case's mean drops by more than MaxRegression or more than twice that case's own standard error, whichever is larger (a drop
//     inside the noise is not evidence of harm);
//   - (c) the held-out mean drops by no more than HeldOutSlack or twice its standard error, whichever is larger;
//   - (d) the failure rate (failed samples over samples) does not rise.
//
// A bootstrap was considered: with two or three samples per case it resamples the same few numbers and claims a precision it does not
// have; the standard error is as honest and can be checked by hand.
func Accept(before, after *RunResult, r Rule) Decision {
	noise := r.NoiseSD
	if noise <= 0 {
		noise = DefaultNoiseSD
	}
	d := Decision{Accepted: true}
	fail := func(format string, a ...any) {
		d.Accepted = false
		d.Reasons = append(d.Reasons, "REJECT: "+fmt.Sprintf(format, a...))
	}
	note := func(format string, a ...any) { d.Reasons = append(d.Reasons, fmt.Sprintf(format, a...)) }
	var gains, vars []float64
	var hb, ha []CaseResult
	for _, b := range before.Cases {
		a, found := after.ByCase(b.Case)
		if !found || b.Score == nil || a.Score == nil || b.Calibration != nil {
			continue
		}
		if b.HeldOut {
			hb, ha = append(hb, b), append(ha, a)
			continue
		}
		g := *a.Score - *b.Score
		v := caseVar(b, noise) + caseVar(a, noise)
		gains, vars = append(gains, g), append(vars, v)
		se := math.Sqrt(v)
		note("case %s: %.1f%s -> %.1f%s (%+.1f ± %.1f)", b.Case, *b.Score, sdText(b), *a.Score, sdText(a), g, se)
		if drop := -g; drop > math.Max(r.MaxRegression, 2*se) {
			fail("case %s dropped %.1f, more than %.1f and more than twice its noise (%.1f)", b.Case, drop, r.MaxRegression, 2*se)
		}
	}
	if len(gains) == 0 {
		fail("no case was scored in both runs")
		return d
	}
	var sum, v float64
	for i := range gains {
		sum, v = sum+gains[i], v+vars[i]
	}
	k := float64(len(gains))
	d.Cases = len(gains)
	d.Gain, d.GainSE = math.Round(100*sum/k)/100, math.Round(100*math.Sqrt(v)/k)/100
	switch {
	case d.Gain < r.MinGain:
		fail("the mean gain %+.2f is below the minimum %+.2f", d.Gain, r.MinGain)
	case d.Gain <= 2*d.GainSE:
		fail("the mean gain %+.2f is within the noise: it must exceed twice its standard error (2 × %.2f = %.2f)", d.Gain, d.GainSE, 2*d.GainSE)
	default:
		note("the mean gain %+.2f exceeds twice its standard error (2 × %.2f = %.2f) and the minimum %+.2f", d.Gain, d.GainSE, 2*d.GainSE, r.MinGain)
	}
	if len(hb) > 0 {
		mb, seb := meanOfCases(hb, noise)
		ma, sea := meanOfCases(ha, noise)
		se := math.Sqrt((*seb)*(*seb) + (*sea)*(*sea))
		if drop := *mb - *ma; drop > math.Max(r.HeldOutSlack, 2*se) {
			fail("the held-out mean dropped %.1f (%.1f -> %.1f), more than %.1f and more than twice its noise (%.1f)", drop, *mb, *ma, r.HeldOutSlack, 2*se)
		} else {
			note("the held-out mean held: %.1f -> %.1f (± %.1f)", *mb, *ma, se)
		}
	}
	if after.FailureRate > before.FailureRate {
		fail("the failure rate rose from %.0f %% to %.0f %% (%d of %d samples failed)", 100*before.FailureRate, 100*after.FailureRate, after.Failures, after.Samples)
	} else {
		note("the failure rate did not rise: %.0f %% -> %.0f %%", 100*before.FailureRate, 100*after.FailureRate)
	}
	return d
}

func sdText(c CaseResult) string {
	if c.SD != nil {
		return fmt.Sprintf(" ± %.1f (n=%d)", *c.SD, c.N)
	}
	return fmt.Sprintf(" (n=%d)", c.N)
}

// ScoreText is a case's score as "mean ± sd (n)" for a report.
func ScoreText(c CaseResult) string {
	if c.Score == nil {
		return "-"
	}
	s := fmt.Sprintf("%.1f", *c.Score)
	if c.SD != nil {
		s += fmt.Sprintf(" ± %.1f", *c.SD)
	}
	if len(c.Samples) > 1 {
		s += fmt.Sprintf(" (n=%d)", c.N)
	}
	if len(c.Failures) > 0 {
		s += fmt.Sprintf(" [%d failed]", len(c.Failures))
	}
	return s
}

// MeanText is a run's mean as "mean ± se".
func MeanText(m, se *float64) string {
	if m == nil {
		return "-"
	}
	if se != nil && *se > 0 {
		return fmt.Sprintf("%.1f ± %.1f", *m, *se)
	}
	return fmt.Sprintf("%.1f", *m)
}

// failureList is the failed samples of a run, for a report.
func failureList(r *RunResult) []string {
	var out []string
	for _, c := range r.Cases {
		for _, f := range c.Failures {
			out = append(out, c.Case+" "+f)
		}
	}
	return out
}
