package analysis

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// The kinds of moment (moments.kind).
const (
	KindRep  = "rep"
	KindHold = "hold"
	KindDead = "dead"
	KindShot = "shot"
)

// Version is the version of the analysis (the algorithm and its parameters). It is stored with the result; a change of the
// numbers or the method bumps it, and `cfctl assets analyze --all` re-runs the assets of an older version.
const Version = 1

// Moment is a stretch of a clip the analysis marked: one rep, a still hold, dead time, or a whole shot.
type Moment struct {
	Kind    string  `json:"kind"`
	StartMs int     `json:"start_ms"`
	EndMs   int     `json:"end_ms"`
	Score   float64 `json:"score"`
	// Description is the short reason the editor reads: "rep 3 of 8, clean, full range".
	Description string         `json:"description"`
	Labels      []string       `json:"labels,omitempty"`
	Attrs       map[string]any `json:"attrs,omitempty"`
}

// Hints are what is known about the clip besides its pixels: its words (title, file name, tags from the quick look). Posing is
// a still thing, so a clip that says it is posing holds are what to look for; a rep clip's stillness is only a rest.
type Hints struct {
	Words []string
}

// poseWords are the words of a title, file name or tag that say the clip is about posing, not about repeated movement.
var poseWords = []string{"pose", "posing", "flex", "flexing", "front double", "back double", "double biceps", "lat spread", "most muscular", "side chest", "side triceps", "physique", "mandatory"}

// Posing reports whether the hints say the clip is of posing.
func (h Hints) Posing() bool {
	for _, w := range h.Words {
		w = strings.ToLower(w)
		for _, p := range poseWords {
			if strings.Contains(w, p) {
				return true
			}
		}
	}
	return false
}

// Summary is the one-glance result of an analysis, stored with it and read by the editor's run context.
type Summary struct {
	Version   int `json:"version"`
	DurMs     int `json:"duration_ms"`
	Shots     int `json:"shots"`
	Sets      int `json:"sets"`
	Reps      int `json:"reps"`
	CleanReps int `json:"clean_reps"`
	// BestRep is the highest-scoring rep.
	BestRep *Moment `json:"best_rep,omitempty"`
	// PeriodMs is the median rep duration over all sets.
	PeriodMs      int     `json:"period_ms,omitempty"`
	DeadMs        int     `json:"dead_ms"`
	Holds         int     `json:"holds"`
	HoldMs        int     `json:"hold_ms"`
	LongestHoldMs int     `json:"longest_hold_ms"`
	MeanY         float64 `json:"mean_y"`
	Level         string  `json:"exposure_level"`
	Gain          float64 `json:"exposure_gain"`
	// Sharpness is the clip's mean edge energy.
	Sharpness float64 `json:"sharpness"`
}

// Analysis is everything the analysis of one clip produces.
type Analysis struct {
	Series  Series   `json:"-"`
	Shots   []Shot   `json:"shots"`
	Moments []Moment `json:"moments"`
	Summary Summary  `json:"summary"`
}

// Analyze runs the whole analysis on the raw output of the ffmpeg pass.
func Analyze(raw Raw, h Hints, p Params, sp ShotParams) Analysis {
	s := BuildSeries(raw)
	a := Analysis{Series: s, Shots: DetectShots(s, sp)}
	if s.Len() == 0 {
		return a
	}
	sharpRef := quantile(s.Sharp, 0.9)
	posing := h.Posing()

	type shotResult struct {
		sets []Set
	}
	results := make([]shotResult, len(a.Shots))
	totalReps := 0
	for i, sh := range a.Shots {
		res := FindRepsIn(raw, s, sh.From, sh.To, p)
		results[i].sets = res.Sets
		for _, set := range res.Sets {
			totalReps += len(set.Reps)
		}
	}
	clipHasReps := totalReps > 0

	setNo := 0
	var allDur []float64
	for i, sh := range a.Shots {
		covered := make([]bool, sh.To-sh.From)
		for _, set := range results[i].sets {
			setNo++
			allDur = append(allDur, set.PeriodMs)
			for j, rp := range set.Reps {
				a.Moments = append(a.Moments, repMoment(s, sh, rp, setNo, j+1, len(set.Reps), set, sharpRef))
			}
			for k := max(set.StartIdx, sh.From); k < min(set.EndIdx, sh.To); k++ {
				covered[k-sh.From] = true
			}
		}
		a.Moments = append(a.Moments, restMoments(s, sh, covered, results[i].sets, clipHasReps, posing, sharpRef, p)...)
		a.Moments = append(a.Moments, shotMoment(sh, len(results[i].sets), clipHasReps))
	}
	sort.SliceStable(a.Moments, func(i, j int) bool {
		if a.Moments[i].StartMs != a.Moments[j].StartMs {
			return a.Moments[i].StartMs < a.Moments[j].StartMs
		}
		return a.Moments[i].Kind < a.Moments[j].Kind
	})
	a.Summary = summarise(s, a, setNo, allDur)
	return a
}

func repMoment(s Series, sh Shot, rp Rep, set, n, of int, st Set, sharpRef float64) Moment {
	from, to := rp.StartIdx, min(rp.EndIdx+1, s.Len())
	meanY := KeyLuma(NormLuma(mean(s.Luma[from:to])), NormLuma(mean(s.High[from:to])))
	rel := 0.0
	if sharpRef > 0 {
		rel = math.Min(mean(s.Sharp[from:to])/sharpRef, 1.5)
	}
	complete := clamp01(rp.Range / 0.85)
	score := 0.4*complete + 0.25*rp.Regularity + 0.2*SharpnessScore(rel) + 0.15*ExposureScore(meanY)

	var notes []string
	switch {
	case rp.Range >= 0.85 && rp.Regularity >= 0.6:
		notes = append(notes, "clean, full range")
	case rp.Range < 0.85:
		notes = append(notes, fmt.Sprintf("short range (%d%% of the set's typical)", int(math.Round(rp.Range*100))))
	}
	if rp.Regularity < 0.6 {
		notes = append(notes, "uneven tempo")
	}
	if SharpnessScore(rel) < 0.4 {
		notes = append(notes, "soft or blurred")
	}
	if h := NewExposureHint(meanY); h.Level != "ok" {
		notes = append(notes, h.Level)
	}
	return Moment{
		Kind: KindRep, StartMs: rp.StartIdx * s.StepMs, EndMs: (rp.EndIdx) * s.StepMs, Score: round2(score),
		Description: fmt.Sprintf("rep %d of %d, %s", n, of, strings.Join(notes, ", ")),
		Attrs: map[string]any{
			"set": set, "rep": n, "reps_in_set": of, "shot": sh.Idx,
			"peak_ms":   rp.PeakIdx * s.StepMs,
			"period_ms": int(math.Round(st.PeriodMs)),
			"range":     round2(rp.Range), "regularity": round2(rp.Regularity),
			"sharpness_rel": round2(rel), "mean_y": round2(meanY),
		},
	}
}

// restMoments marks what is outside the sets of a shot: still stretches (a hold when the clip is of posing or has no reps at all,
// otherwise dead time, the rest between sets) and stretches of movement that is not periodic (dead time).
func restMoments(s Series, sh Shot, covered []bool, sets []Set, clipHasReps, posing bool, sharpRef float64, p Params) []Moment {
	n := sh.To - sh.From
	if n < 2 {
		return nil
	}
	mot := smooth(s.Motion[sh.From:sh.To], 10) // a second
	floor := quantile(mot, 0.05)
	thr := math.Min(math.Max(p.StillMult*floor, p.StillFloor), 0.8)
	const (
		inSet  = 0
		still  = 1
		moving = 2
	)
	cls := make([]int, n)
	for i := range cls {
		switch {
		case covered[i]:
			cls[i] = inSet
		case mot[i] < thr:
			cls[i] = still
		default:
			cls[i] = moving
		}
	}
	// The movement that leads into a set and out of it (the lifter getting into position, the last half rep) belongs to the set:
	// moving samples up to one rep after its edges are not dead time.
	for _, set := range sets {
		reach := int(set.PeriodMs) / s.StepMs
		for k := set.EndIdx - sh.From; k < min(set.EndIdx-sh.From+reach, n); k++ {
			if k >= 0 && cls[k] == moving {
				cls[k] = inSet
			} else if k >= 0 {
				break
			}
		}
		for k := set.StartIdx - sh.From - 1; k >= max(set.StartIdx-sh.From-reach, 0); k-- {
			if cls[k] == moving {
				cls[k] = inSet
			} else {
				break
			}
		}
	}
	minStill, minDead := p.samples(p.MinStillMs), p.samples(p.MinDeadMs)
	var out []Moment
	for i := 0; i < n; {
		j := i
		for j < n && cls[j] == cls[i] {
			j++
		}
		a, b := sh.From+i, sh.From+j
		switch {
		case cls[i] == still && j-i >= minStill:
			if posing || !clipHasReps {
				out = append(out, holdMoment(s, sh, a, b, thr, sharpRef))
			} else {
				out = append(out, deadMoment(s, sh, a, b, "still between sets"))
			}
		case cls[i] == moving && j-i >= minDead:
			out = append(out, deadMoment(s, sh, a, b, "movement that is not a repeating rep"))
		}
		i = j
	}
	return out
}

func holdMoment(s Series, sh Shot, a, b int, thr, sharpRef float64) Moment {
	meanY := KeyLuma(NormLuma(mean(s.Luma[a:b])), NormLuma(mean(s.High[a:b])))
	rel := 0.0
	if sharpRef > 0 {
		rel = math.Min(mean(s.Sharp[a:b])/sharpRef, 1.5)
	}
	steady := clamp01(1 - mean(s.Motion[a:b])/math.Max(thr, 1e-9))
	long := clamp01(float64(b-a) * float64(s.StepMs) / 3000)
	score := 0.3*steady + 0.25*SharpnessScore(rel) + 0.25*ExposureScore(meanY) + 0.2*long
	notes := []string{fmt.Sprintf("steady for %.1f s", float64(b-a)*float64(s.StepMs)/1000)}
	if SharpnessScore(rel) >= 0.7 {
		notes = append(notes, "sharp")
	}
	if h := NewExposureHint(meanY); h.Level == "ok" {
		notes = append(notes, "well lit")
	} else {
		notes = append(notes, h.Level)
	}
	return Moment{
		Kind: KindHold, StartMs: a * s.StepMs, EndMs: b * s.StepMs, Score: round2(score), Description: strings.Join(notes, ", "),
		Attrs: map[string]any{"shot": sh.Idx, "mean_y": round2(meanY), "sharpness_rel": round2(rel), "motion": round2(mean(s.Motion[a:b]))},
	}
}

func deadMoment(s Series, sh Shot, a, b int, why string) Moment {
	return Moment{
		Kind: KindDead, StartMs: a * s.StepMs, EndMs: b * s.StepMs, Score: 0,
		Description: fmt.Sprintf("dead time, %.1f s: %s", float64(b-a)*float64(s.StepMs)/1000, why),
		Attrs:       map[string]any{"shot": sh.Idx, "reason": why},
	}
}

// shotMoment is the moment of a whole shot, scored by how usable its picture is and what it holds.
func shotMoment(sh Shot, sets int, clipHasReps bool) Moment {
	content := 0.3
	switch {
	case sets > 0:
		content = 1
	case !clipHasReps:
		content = 0.6
	}
	score := 0.4*ExposureScore(sh.Exposure.MeanY) + 0.3*SharpnessScore(sh.SharpnessRel) + 0.3*content
	return Moment{
		Kind: KindShot, StartMs: sh.StartMs, EndMs: sh.EndMs, Score: round2(score),
		Description: fmt.Sprintf("shot %d, %.1f s, %s exposure", sh.Idx+1, float64(sh.EndMs-sh.StartMs)/1000, sh.Exposure.Level),
		Attrs: map[string]any{
			"shot": sh.Idx, "sets": sets, "exposure": sh.Exposure, "sharpness": sh.Sharpness, "sharpness_rel": sh.SharpnessRel,
			"motion": sh.Motion, "contrast": sh.Contrast,
		},
	}
}

func summarise(s Series, a Analysis, sets int, periods []float64) Summary {
	sum := Summary{Version: Version, DurMs: s.DurationMs(), Shots: len(a.Shots), Sets: sets, Sharpness: round2(mean(s.Sharp))}
	meanY := KeyLuma(NormLuma(mean(s.Luma)), NormLuma(mean(s.High)))
	h := NewExposureHint(meanY)
	sum.MeanY, sum.Level, sum.Gain = h.MeanY, h.Level, h.Gain
	for _, m := range a.Moments {
		switch m.Kind {
		case KindRep:
			sum.Reps++
			if m.Score >= 0.7 {
				sum.CleanReps++
			}
			if sum.BestRep == nil || m.Score > sum.BestRep.Score {
				c := m
				sum.BestRep = &c
			}
		case KindDead:
			sum.DeadMs += m.EndMs - m.StartMs
		case KindHold:
			sum.Holds++
			d := m.EndMs - m.StartMs
			sum.HoldMs += d
			sum.LongestHoldMs = max(sum.LongestHoldMs, d)
		}
	}
	if len(periods) > 0 {
		sum.PeriodMs = int(math.Round(quantile(periods, 0.5)))
	}
	return sum
}

// Line is the one-line summary of a clip for the editor's context: "2 sets, 14 reps (best at 31.2 s), 31 s dead time".
func (s Summary) Line() string {
	var parts []string
	switch {
	case s.Reps > 0:
		p := fmt.Sprintf("%d %s, %d reps", s.Sets, plural(s.Sets, "set", "sets"), s.Reps)
		if s.BestRep != nil {
			p += fmt.Sprintf(" (best at %.1f s, score %.2f)", float64(s.BestRep.StartMs)/1000, s.BestRep.Score)
		}
		if s.PeriodMs > 0 {
			p += fmt.Sprintf(", about %.1f s each", float64(s.PeriodMs)/1000)
		}
		parts = append(parts, p)
	case s.Holds > 0:
		parts = append(parts, fmt.Sprintf("no reps, %d %s (longest %.1f s)", s.Holds, plural(s.Holds, "hold", "holds"), float64(s.LongestHoldMs)/1000))
	default:
		parts = append(parts, "no reps or holds found")
	}
	if s.Reps > 0 && s.Holds > 0 {
		parts = append(parts, fmt.Sprintf("%d %s", s.Holds, plural(s.Holds, "hold", "holds")))
	}
	if s.DeadMs >= 1000 {
		parts = append(parts, fmt.Sprintf("%d s dead time", int(math.Round(float64(s.DeadMs)/1000))))
	}
	if s.Shots > 1 {
		parts = append(parts, fmt.Sprintf("%d shots", s.Shots))
	}
	switch s.Level {
	case "dark":
		parts = append(parts, fmt.Sprintf("dark (brightness %.0f%%, a gain of x%.1f would be needed)", s.MeanY*100, s.Gain))
	case "bright":
		parts = append(parts, fmt.Sprintf("bright (brightness %.0f%%)", s.MeanY*100))
	}
	return strings.Join(parts, "; ")
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
