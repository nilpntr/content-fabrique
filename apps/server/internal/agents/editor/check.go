package editor

import (
	"cmp"
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/nilpntr/content-fabrique/internal/analysis"
	"github.com/nilpntr/content-fabrique/internal/timeline"
)

// check_edit: the objective checks of an edit (docs/design/edit-style-v1.md, step 5). Everything here is computed in Go from the
// stored timeline version and the stored clip analysis: no model, no picture. The editor runs it after render_preview and revises
// once when a check fails; `cfctl eval editor` runs the same function over any stored version, so two runs can be compared with
// the same numbers.
//
// A check passes, fails (with a one-line reason that says what to change) or is skipped (it does not apply, or the data it needs is
// not there). The thresholds are constants and a per-genre table: they are a bar, not a taste.

// CheckStatus is the outcome of one check.
type CheckStatus string

// The outcomes.
const (
	CheckPass CheckStatus = "pass"
	CheckFail CheckStatus = "fail"
	CheckSkip CheckStatus = "skip"
)

// The check ids, in the order a report lists them.
const (
	CheckHookLength    = "hook_length"
	CheckHookTitle     = "hook_title"
	CheckShotPacing    = "shot_pacing"
	CheckShotVariety   = "shot_variety"
	CheckPaceBuilds    = "pace_builds"
	CheckRepBoundaries = "rep_boundaries"
	CheckDeadTime      = "dead_time"
	CheckEffectDensity = "effect_density"
	CheckTextSafe      = "text_safe_zone"
	CheckDuration      = "duration"
	CheckBrightness    = "brightness_jump"
	CheckCrushed       = "crushed"
	CheckBeatSync      = "beat_sync"
)

// The thresholds that do not depend on the kind of video.
const (
	hookMaxMs         = 2000
	hookTitleByMs     = 500
	repToleranceMs    = 150
	repEdgesShare     = 0.85
	deadShareMax      = 0.05
	effectsMax        = 0.5
	rampsMax          = 4
	brightnessJumpMax = 0.25
	// brightnessRatioMin and brightnessRatioDelta: in low-key footage the p90 luma of every shot is small, so an absolute jump of 0.25
	// never happens; a cut is also a jump when the lit part of one shot is 1.8 times (0.85 stops) the other's and they differ by at
	// least 0.10 (the posing clip of the founder's footage is 1.9 times the gym clips).
	brightnessRatioMax   = 1.8
	brightnessRatioDelta = 0.10
	// crushedRatio is how much of its source's brightness a rendered shot must keep (the p90 or the mean of the luma): the look that
	// crushed the founder's footage left 0.24 to 0.40 of the p90 and none of the mean, ungraded renders keep all of it. crushedBlack is
	// a rendered p90 under which a shot is near black whatever its source. minSourceP90 and minSourceMean keep the ratios to shots
	// whose source is bright enough for a ratio to mean something.
	crushedRatio        = 0.7
	crushedBlack        = 0.04
	minSourceP90        = 0.06
	minSourceMean       = 0.03
	beatToleranceFrames = 2
	beatShareMin        = 0.7
	// durationLoPct and durationHiPct are the share of the plan's target length that a video may be: the same tolerance as plan_edit's
	// note about a shot list that does not match its target.
	durationLoPct        = 75
	durationHiPct        = 125
	defaultMinDurationMs = 15_000
	defaultMaxDurationMs = 45_000
	paceBuildsMinShots   = 6
	paceBuildsSlack      = 1.15
	varietyMinCV         = 0.12
)

// Check is one result.
type Check struct {
	ID     string      `json:"id"`
	Status CheckStatus `json:"status"`
	// Detail is one line: the numbers, and what to change when the check fails.
	Detail string `json:"detail"`
}

// Span is a stretch of a clip, in the clip's own milliseconds.
type Span struct{ StartMs, EndMs int64 }

// AssetAnalysis is what check_edit knows about one clip from its stored analysis.
type AssetAnalysis struct {
	// Analysed says the clip has an analysis at all (its moments are there, even when there are none of a kind).
	Analysed bool
	// Reps, Holds and Dead are the moments of those kinds.
	Reps, Holds, Dead []Span
	// Series holds the luma and 90th percentile luma of the clip, when the stored analysis has them.
	Series    analysis.Series
	HasSeries bool
}

// CheckInput is what Check reads.
type CheckInput struct {
	Version int
	Spec    *timeline.Spec
	// Plan is the run's creative plan: it names the content type (which sets the thresholds) and the target length. Nil is a general
	// video with the default length range.
	Plan *Plan
	// Assets holds the analysis of the clips, by asset id. A clip that is not in it, or not analysed, is skipped by the checks that
	// need the analysis.
	Assets map[string]AssetAnalysis
	// Issues are the validation issues of the version (the safe zone of the texts is the validator's).
	Issues []timeline.Issue
	// Rendered is the luma of the version's preview render (a mean in Luma and a 90th percentile in High per sample, as the analysis
	// series), nil when there is no preview of the version yet: the checks that look at the rendered picture do not apply then.
	Rendered *analysis.Series
}

// Metrics are the numbers behind the checks, for the run feed and for comparing runs.
type Metrics struct {
	DurationMs int64 `json:"duration_ms"`
	Shots      int   `json:"shots"`
	// ShotMs are the shortest, the median and the longest shot, in output milliseconds.
	ShotMinMs    int64 `json:"shot_min_ms"`
	ShotMedianMs int64 `json:"shot_median_ms"`
	ShotMaxMs    int64 `json:"shot_max_ms"`
	// LongestEqualRun is the most shots of (nearly) the same length in a row.
	LongestEqualRun int `json:"longest_equal_run"`
	// RepEdges is how many of the shot edges on clips with reps are on a rep boundary, out of how many there are.
	RepEdgesOn    int `json:"rep_edges_on"`
	RepEdgesTotal int `json:"rep_edges_total"`
	// DeadShare is the share of the screen time that is dead time (0 to 1), -1 when it could not be measured.
	DeadShare float64 `json:"dead_share"`
	// Effects counts the motion presets, the transitions that are not cuts and the speed ramps; Ramps the speed ramps alone.
	Effects         int     `json:"effects"`
	Ramps           int     `json:"ramps"`
	EffectsPerCut   float64 `json:"effects_per_cut"`
	CutsOnBeat      int     `json:"cuts_on_beat"`
	Cuts            int     `json:"cuts"`
	BrightnessJump  float64 `json:"brightness_max_jump"`
	BrightnessRange float64 `json:"brightness_range"`
	// RenderedKept is the lowest share of its source's p90 luma that a rendered shot keeps (1 is as bright as the source), 0 when the
	// preview was not measured.
	RenderedKept float64 `json:"rendered_kept"`
}

// Report is the result of Check.
type Report struct {
	Version     int     `json:"version"`
	ContentType string  `json:"content_type"`
	TargetS     int     `json:"target_length_s,omitempty"`
	Checks      []Check `json:"checks"`
	Metrics     Metrics `json:"metrics"`
}

// Counts returns how many checks passed, failed and were skipped.
func (r Report) Counts() (pass, fail, skip int) {
	for _, c := range r.Checks {
		switch c.Status {
		case CheckPass:
			pass++
		case CheckFail:
			fail++
		default:
			skip++
		}
	}
	return pass, fail, skip
}

// Failed lists the ids of the failing checks.
func (r Report) Failed() []string {
	var out []string
	for _, c := range r.Checks {
		if c.Status == CheckFail {
			out = append(out, c.ID)
		}
	}
	return out
}

// limits are the thresholds that depend on the kind of video.
type limits struct {
	medianShotMs int64   // the median shot may not be longer
	minEffects   float64 // effects per cut, at least (0: no lower bound)
	beatGrid     bool    // the video follows a beat grid
	paceBuilds   bool    // the cuts speed up toward the end
}

var typeLimits = map[string]limits{
	TypeWorkout:  {3000, 0.15, true, true},
	TypeUnboxing: {3500, 0.15, true, true},
	TypeVlog:     {4000, 0.15, true, true},
	TypeGeneral:  {3500, 0.15, true, true},
	TypeTutorial: {5000, 0, false, false},
	TypeTalking:  {6000, 0, false, false},
}

// RunChecks runs the checks.
func RunChecks(in CheckInput) Report {
	typ := TypeGeneral
	targetS := 0
	if in.Plan != nil && ValidContentType(in.Plan.ContentType) {
		typ, targetS = in.Plan.ContentType, in.Plan.TargetLengthS
	}
	r := Report{Version: in.Version, ContentType: typ, TargetS: targetS}
	main, _, ok := timeline.MainTrack(in.Spec)
	if !ok || len(main.Items) == 0 {
		r.Checks = []Check{{ID: CheckDuration, Status: CheckFail, Detail: "the timeline has no clips"}}
		return r
	}
	items := slices.Clone(main.Items)
	slices.SortStableFunc(items, func(a, b timeline.VideoItem) int { return cmp.Compare(a.AtMs, b.AtMs) })
	lim := typeLimits[typ]
	if in.Plan != nil && in.Plan.TempoBPM == 0 {
		lim.beatGrid = false // the plan has no beat grid (a video that follows speech or a mood): the cuts are not judged against one
	}
	c := &checker{in: in, items: items, lim: lim, typ: typ, m: &r.Metrics}
	c.total = timeline.TotalDuration(in.Spec)
	// A shot's length is what it is on screen: from where it starts to where the next one starts, so the overlap of a transition
	// into the next shot is not counted (a 1.95 s hook with a 250 ms whip out of it is still a 1.95 s hook). The last shot runs to
	// the end of the video.
	c.durs = make([]int64, len(items))
	for i, it := range items {
		if i+1 < len(items) {
			c.durs[i] = items[i+1].AtMs - it.AtMs
		} else {
			c.durs[i] = c.total - it.AtMs
		}
		c.durs[i] = max(c.durs[i], 0)
	}
	r.Metrics.DurationMs, r.Metrics.Shots = c.total, len(items)
	r.Checks = []Check{
		c.hookLength(), c.hookTitle(), c.shotPacing(), c.shotVariety(), c.paceBuilds(), c.repBoundaries(), c.deadTime(),
		c.effectDensity(), c.textSafe(), c.duration(targetS), c.brightness(), c.crushed(), c.beatSync(),
	}
	return r
}

type checker struct {
	in    CheckInput
	items []timeline.VideoItem
	durs  []int64
	total int64
	lim   limits
	typ   string
	m     *Metrics
}

func (c *checker) pass(id, format string, a ...any) Check {
	return Check{ID: id, Status: CheckPass, Detail: fmt.Sprintf(format, a...)}
}
func (c *checker) fail(id, format string, a ...any) Check {
	return Check{ID: id, Status: CheckFail, Detail: fmt.Sprintf(format, a...)}
}
func (c *checker) skip(id, format string, a ...any) Check {
	return Check{ID: id, Status: CheckSkip, Detail: fmt.Sprintf(format, a...)}
}

func (c *checker) hookLength() Check {
	d := c.durs[0]
	if d > hookMaxMs {
		return c.fail(CheckHookLength, "the first shot (%s) is %s long; the hook should be at most %s. Trim it to its strongest 1 to 2 seconds, or speed it up", c.items[0].ID, secs2(d), secs2(hookMaxMs))
	}
	return c.pass(CheckHookLength, "the first shot is %s long (at most %s)", secs2(d), secs2(hookMaxMs))
}

func (c *checker) hookTitle() Check {
	var first int64 = -1
	for _, t := range c.in.Spec.Tracks {
		switch t := t.(type) {
		case *timeline.CaptionsTrack:
			if t.Enabled {
				return c.pass(CheckHookTitle, "captions are on, so there is text from the first word")
			}
		case *timeline.TextTrack:
			for _, it := range t.Items {
				if first < 0 || it.AtMs < first {
					first = it.AtMs
				}
			}
		}
	}
	switch {
	case first < 0:
		return c.fail(CheckHookTitle, "there is no title; add a few bold words that start by %s", secs2(hookTitleByMs))
	case first > hookTitleByMs:
		return c.fail(CheckHookTitle, "the first text starts at %s; the title should be on screen by %s", secs2(first), secs2(hookTitleByMs))
	}
	return c.pass(CheckHookTitle, "the title starts at %s (by %s)", secs2(first), secs2(hookTitleByMs))
}

func (c *checker) shotStats() (minMs, medMs, maxMs int64) {
	s := slices.Clone(c.durs)
	slices.Sort(s)
	return s[0], s[len(s)/2], s[len(s)-1]
}

func (c *checker) shotPacing() Check {
	lo, med, hi := c.shotStats()
	c.m.ShotMinMs, c.m.ShotMedianMs, c.m.ShotMaxMs = lo, med, hi
	if med > c.lim.medianShotMs {
		return c.fail(CheckShotPacing, "the median shot is %s long (at most %s for a %s video; shortest %s, longest %s). Make most shots 1 to 4 beats: take the strongest half of a long shot, or speed it up",
			secs2(med), secs2(c.lim.medianShotMs), c.typ, secs2(lo), secs2(hi))
	}
	return c.pass(CheckShotPacing, "the median shot is %s long (at most %s for a %s video; shortest %s, longest %s)", secs2(med), secs2(c.lim.medianShotMs), c.typ, secs2(lo), secs2(hi))
}

// nearlyEqual says two shot lengths are the same length: within 8 % or 100 ms.
func nearlyEqual(a, b int64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d <= max(100, max(a, b)*8/100)
}

func (c *checker) shotVariety() Check {
	run, best, bestAt := 1, 1, 0
	for i := 1; i < len(c.durs); i++ {
		if nearlyEqual(c.durs[i], c.durs[i-1]) {
			run++
			if run > best {
				best, bestAt = run, i-run+1
			}
		} else {
			run = 1
		}
	}
	c.m.LongestEqualRun = best
	if best > 2 {
		return c.fail(CheckShotVariety, "%d shots in a row are about %s long (%s to %s); never more than two of the same length. Change the length of one of them", best, secs2(c.durs[bestAt]), c.items[bestAt].ID, c.items[bestAt+best-1].ID)
	}
	if len(c.durs) >= 5 {
		var sum, sq float64
		for _, d := range c.durs {
			sum += float64(d)
		}
		mean := sum / float64(len(c.durs))
		for _, d := range c.durs {
			sq += (float64(d) - mean) * (float64(d) - mean)
		}
		if cv := math.Sqrt(sq/float64(len(c.durs))) / mean; cv < varietyMinCV {
			return c.fail(CheckShotVariety, "the shots are all close to the same length (spread %.0f %%); mix 1 to 2 beat hype shots with a few longer ones", cv*100)
		}
	}
	return c.pass(CheckShotVariety, "the longest run of same-length shots is %d (at most 2)", best)
}

func medianOf(x []int64) int64 {
	s := slices.Clone(x)
	slices.Sort(s)
	return s[len(s)/2]
}

func (c *checker) paceBuilds() Check {
	n := len(c.durs)
	if !c.lim.paceBuilds {
		return c.skip(CheckPaceBuilds, "does not apply to a %s video", c.typ)
	}
	if n < paceBuildsMinShots {
		return c.skip(CheckPaceBuilds, "only %d shots", n)
	}
	first, second := medianOf(c.durs[:n/2]), medianOf(c.durs[n/2:n-1]) // the last shot is held longer on purpose
	if float64(second) > float64(first)*paceBuildsSlack {
		return c.fail(CheckPaceBuilds, "the cuts slow down: the median shot is %s in the first half and %s in the second. The cuts should speed up toward the end", secs2(first), secs2(second))
	}
	return c.pass(CheckPaceBuilds, "the median shot is %s in the first half and %s in the second", secs2(first), secs2(second))
}

func (c *checker) asset(it timeline.VideoItem) (AssetAnalysis, bool) {
	a, ok := c.in.Assets[it.AssetID]
	return a, ok && a.Analysed
}

func (c *checker) repBoundaries() Check {
	type miss struct {
		text string
		off  int64
	}
	var misses []miss
	on, total := 0, 0
	for _, it := range c.items {
		a, ok := c.asset(it)
		if !ok || len(a.Reps) == 0 {
			continue
		}
		for _, e := range []struct {
			name string
			at   int64
		}{{"starts", it.Source.InMs}, {"ends", it.Source.OutMs}} {
			total++
			if near, d := nearestRepBoundary(a, e.at); d <= repToleranceMs || insideHold(a, e.at) {
				on++
			} else {
				misses = append(misses, miss{fmt.Sprintf("%s %s at %d ms (nearest rep boundary %d ms)", it.ID, e.name, e.at, near), d})
			}
		}
	}
	c.m.RepEdgesOn, c.m.RepEdgesTotal = on, total
	if total == 0 {
		return c.skip(CheckRepBoundaries, "none of the clips used has reps")
	}
	share := float64(on) / float64(total)
	if share < repEdgesShare {
		slices.SortStableFunc(misses, func(a, b miss) int { return cmp.Compare(b.off, a.off) })
		var parts []string
		for _, m := range misses[:min(3, len(misses))] {
			parts = append(parts, m.text)
		}
		return c.fail(CheckRepBoundaries, "%d of %d shot edges on reps are within %d ms of a rep start, end or turning point; off: %s. Move those edges to a boundary from find_moments", on, total, repToleranceMs, strings.Join(parts, "; "))
	}
	return c.pass(CheckRepBoundaries, "%d of %d shot edges on reps are within %d ms of a rep start, end or turning point", on, total, repToleranceMs)
}

// nearestRepBoundary is the rep boundary (a rep's start, its end or its midpoint, the turning point of the opposite phase)
// nearest to t, and how far it is.
func nearestRepBoundary(a AssetAnalysis, t int64) (at, dist int64) {
	dist = math.MaxInt64
	for _, r := range a.Reps {
		for _, b := range []int64{r.StartMs, r.EndMs, (r.StartMs + r.EndMs) / 2} {
			if d := absI(b - t); d < dist {
				at, dist = b, d
			}
		}
	}
	return at, dist
}

// insideHold says t is within a hold (a steady pose): a cut there is not in the middle of a rep.
func insideHold(a AssetAnalysis, t int64) bool {
	for _, h := range a.Holds {
		if t >= h.StartMs-repToleranceMs && t <= h.EndMs+repToleranceMs {
			return true
		}
	}
	return false
}

func absI(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

func (c *checker) deadTime() Check {
	c.m.DeadShare = -1
	var deadOut, measured float64
	worst, worstMs := "", int64(0)
	for i, it := range c.items {
		a, ok := c.asset(it)
		if !ok {
			continue
		}
		span := it.Source.OutMs - it.Source.InMs
		if span <= 0 {
			continue
		}
		// The dead stretches are mapped through the clip's own time (its speed, or its speed ramp: a ramp that slows the peak and
		// rushes the rest plays a dead stretch at the rush for far less than its share of the source), and scaled to the part of the
		// clip that is on screen.
		var dead float64
		for _, d := range a.Dead {
			lo, hi := max(d.StartMs, it.Source.InMs), min(d.EndMs, it.Source.OutMs)
			if hi > lo {
				dead += it.OutOffsetMs(float64(hi-it.Source.InMs)) - it.OutOffsetMs(float64(lo-it.Source.InMs))
			}
		}
		full := float64(timeline.OutputDuration(it))
		if full <= 0 {
			continue
		}
		measured += float64(c.durs[i])
		out := dead * float64(c.durs[i]) / full
		deadOut += out
		if int64(out) > worstMs {
			worst, worstMs = it.ID, int64(out)
		}
	}
	if measured == 0 {
		return c.skip(CheckDeadTime, "none of the clips used has been analysed")
	}
	share := deadOut / measured
	c.m.DeadShare = math.Round(share*1000) / 1000
	if share > deadShareMax {
		return c.fail(CheckDeadTime, "%.0f %% of the screen time (%s) is dead time, the most in %s; cut it out (find_moments lists the dead time to skip)", share*100, secs2(int64(deadOut)), worst)
	}
	return c.pass(CheckDeadTime, "%.0f %% of the screen time is dead time (at most %.0f %%)", share*100, deadShareMax*100)
}

func (c *checker) effectDensity() Check {
	effects, ramps := 0, 0
	for i, it := range c.items {
		if it.Motion.Active() {
			effects++
		}
		if i > 0 && it.TransitionIn != nil && it.TransitionIn.Type != timeline.TransitionCut && it.TransitionIn.Type != "" {
			effects++
		}
		if it.HasRamp() {
			effects++
			ramps++
		}
	}
	cuts := len(c.items) - 1
	c.m.Effects, c.m.Ramps, c.m.Cuts = effects, ramps, cuts
	if cuts < 1 {
		return c.skip(CheckEffectDensity, "there are no cuts")
	}
	per := float64(effects) / float64(cuts)
	c.m.EffectsPerCut = math.Round(per*100) / 100
	switch {
	case per > effectsMax:
		return c.fail(CheckEffectDensity, "%d effects in %d cuts (%.2f per cut) is too busy; use about one per 3 or 4 cuts and keep the ones on the best moments", effects, cuts, per)
	case ramps > rampsMax:
		return c.fail(CheckEffectDensity, "%d shots have a speed ramp; at most %d, on the shots that deserve it", ramps, rampsMax)
	case c.lim.minEffects > 0 && cuts >= 4 && per < c.lim.minEffects:
		return c.fail(CheckEffectDensity, "%d effects in %d cuts (%.2f per cut) is flat; add a punch_in on a beat, or a whip or flash between scenes (about one per 3 or 4 cuts)", effects, cuts, per)
	}
	return c.pass(CheckEffectDensity, "%d effects in %d cuts (%.2f per cut), %d speed ramps", effects, cuts, per, ramps)
}

func (c *checker) textSafe() Check {
	texts := 0
	for _, t := range c.in.Spec.Tracks {
		if t, ok := t.(*timeline.TextTrack); ok {
			texts += len(t.Items)
		}
	}
	var bad []string
	for _, is := range c.in.Issues {
		if is.Code == timeline.CodeUnsafeText || is.Code == timeline.CodeTextOverlapsCaptions {
			bad = append(bad, fmt.Sprintf("%s at %s", is.Code, is.Path))
		}
	}
	switch {
	case len(bad) > 0:
		return c.fail(CheckTextSafe, "text outside the safe zone: %s", strings.Join(bad[:min(3, len(bad))], "; "))
	case texts == 0:
		return c.skip(CheckTextSafe, "there is no text")
	}
	return c.pass(CheckTextSafe, "%d texts, all inside the safe zones", texts)
}

func (c *checker) duration(targetS int) Check {
	lo, hi := int64(defaultMinDurationMs), int64(defaultMaxDurationMs)
	what := "the default range"
	if targetS > 0 {
		lo, hi = int64(targetS)*10*durationLoPct, int64(targetS)*10*durationHiPct
		what = fmt.Sprintf("the plan's %d s, give or take a quarter", targetS)
	}
	lo, hi = max(lo, timeline.MinTotalMs), min(hi, timeline.MaxTotalMs)
	switch {
	case c.total < lo:
		return c.fail(CheckDuration, "the video is %s long, under %s (%s). Add a shot or lengthen one", secs2(c.total), secs2(lo), what)
	case c.total > hi:
		return c.fail(CheckDuration, "the video is %s long, over %s (%s). Cut a shot or shorten some", secs2(c.total), secs2(hi), what)
	}
	return c.pass(CheckDuration, "the video is %s long (%s to %s: %s)", secs2(c.total), secs2(lo), secs2(hi), what)
}

// renderedShot is the exposure of the rendered picture of shot i, from the middle half of its time on screen (away from the
// transitions at its ends), at least one sample.
func (c *checker) renderedShot(i int) (analysis.Exposure, bool) {
	if c.in.Rendered == nil {
		return analysis.Exposure{}, false
	}
	start, d := c.items[i].AtMs, c.durs[i]
	lo, hi := start+d/4, start+d-d/4
	if hi-lo < int64(c.in.Rendered.StepMs) {
		mid := start + d/2
		lo, hi = mid-int64(c.in.Rendered.StepMs)/2, mid+int64(c.in.Rendered.StepMs)/2
	}
	return c.in.Rendered.ExposureBetween(int(lo), int(hi))
}

const noPreviewNote = "the preview of this version has not been rendered (or could not be read); call render_preview, then check_edit again"

// brightness judges the cuts by what the render shows (the source analysis says how bright the footage is, not the picture: a look
// changes it). A cut is a jump when the lit part of the two shots differs a lot in absolute terms, or by 1.8 times and at least 0.10:
// low-key footage has small numbers everywhere.
func (c *checker) brightness() Check {
	if c.in.Rendered == nil {
		return c.skip(CheckBrightness, "%s", noPreviewNote)
	}
	type lvl struct {
		e  analysis.Exposure
		ok bool
	}
	lv := make([]lvl, len(c.items))
	lo, hi := 2.0, -1.0
	for i := range c.items {
		lv[i].e, lv[i].ok = c.renderedShot(i)
		if lv[i].ok {
			lo, hi = math.Min(lo, lv[i].e.P90), math.Max(hi, lv[i].e.P90)
		}
	}
	worst, pairs := 0.0, 0
	jump := func(a, b analysis.Exposure) (float64, bool) {
		d := analysis.Jump(a, b)
		small, big := math.Min(a.P90, b.P90), math.Max(a.P90, b.P90)
		bad := d > brightnessJumpMax || (d >= brightnessRatioDelta && big >= brightnessRatioMax*math.Max(small, 0.01))
		return d, bad
	}
	badAt, worstBad := -1, 0.0
	for i := 1; i < len(lv); i++ {
		if !lv[i].ok || !lv[i-1].ok {
			continue
		}
		pairs++
		d, bad := jump(lv[i-1].e, lv[i].e)
		if bad && (badAt < 0 || d > worstBad) {
			badAt, worstBad = i, d
		}
		worst = math.Max(worst, d)
	}
	if pairs == 0 {
		return c.skip(CheckBrightness, "no shot of the preview could be measured")
	}
	c.m.BrightnessJump, c.m.BrightnessRange = math.Round(worst*100)/100, math.Round((hi-lo)*100)/100
	if badAt >= 0 {
		return c.fail(CheckBrightness, "the cut from %s to %s jumps %.2f in the rendered brightness of the lit part (p90 luma %.2f to %.2f; a jump is over %.2f, or %.1f times with at least %.2f). Call match_exposure to darken the brighter shot, or pick another moment for one of them",
			c.items[badAt-1].ID, c.items[badAt].ID, analysis.Jump(lv[badAt-1].e, lv[badAt].e), lv[badAt-1].e.P90, lv[badAt].e.P90,
			brightnessJumpMax, brightnessRatioMax, brightnessRatioDelta)
	}
	return c.pass(CheckBrightness, "the largest jump at a cut is %.2f in the rendered p90 luma (the shots range from %.2f to %.2f)", worst, lo, hi)
}

// expectedGain is the gain the clip's own exposure hint is meant to apply (look.match, or the auto preset, with a hint that is not
// too small to matter): the render is allowed to differ from the source by that much.
func expectedGain(it timeline.VideoItem) float64 {
	if it.ExposureEv == nil || it.Look == nil || !it.Look.AppliesExposure() {
		return 1
	}
	ev := *it.ExposureEv
	if math.Abs(ev) < 0.15 {
		return 1
	}
	return math.Exp2(-math.Max(-0.7, math.Min(0.7, ev)))
}

// crushed compares each shot's rendered brightness (the p90 and the mean of the luma) with its source's (the analysis of the part of
// the clip the shot plays), allowing for the exposure hint the clip carries. A look that darkens low-key footage, like the first
// gym_moody, fails it.
func (c *checker) crushed() Check {
	if c.in.Rendered == nil {
		return c.skip(CheckCrushed, "%s", noPreviewNote)
	}
	type bad struct {
		id   string
		text string
		kept float64
	}
	var misses []bad
	measured := 0
	kept := math.Inf(1)
	for i, it := range c.items {
		r, ok := c.renderedShot(i)
		if !ok {
			continue
		}
		measured++
		if r.P90 < crushedBlack {
			misses = append(misses, bad{it.ID, fmt.Sprintf("%s renders at p90 luma %.2f, near black", it.ID, r.P90), 0})
			kept = 0
			continue
		}
		a, has := c.asset(it)
		if !has || !a.HasSeries {
			continue
		}
		src, ok := a.Series.ExposureBetween(int(it.Source.InMs), int(it.Source.OutMs))
		if !ok {
			continue
		}
		g := expectedGain(it)
		if src.P90 >= minSourceP90 {
			ratio := r.P90 / (src.P90 * g)
			kept = math.Min(kept, ratio)
			if ratio < crushedRatio {
				misses = append(misses, bad{it.ID, fmt.Sprintf("%s keeps %.0f %% of its source's p90 luma (%.2f from %.2f)", it.ID, ratio*100, r.P90, src.P90*g), ratio})
				continue
			}
		}
		if src.Mean >= minSourceMean && r.Mean < crushedRatio*src.Mean*g {
			misses = append(misses, bad{it.ID, fmt.Sprintf("%s keeps %.0f %% of its source's mean luma (%.2f from %.2f)", it.ID, 100*r.Mean/(src.Mean*g), r.Mean, src.Mean*g), r.Mean / (src.Mean * g)})
		}
	}
	if measured == 0 {
		return c.skip(CheckCrushed, "no shot of the preview could be measured")
	}
	if !math.IsInf(kept, 1) {
		c.m.RenderedKept = math.Round(kept*100) / 100
	}
	if len(misses) > 0 {
		slices.SortStableFunc(misses, func(a, b bad) int { return cmp.Compare(a.kept, b.kept) })
		var parts []string
		for _, m := range misses[:min(3, len(misses))] {
			parts = append(parts, m.text)
		}
		return c.fail(CheckCrushed, "%d of %d shots render much darker than their footage: %s. The look is crushing the shadows; set_look none, or use auto (a lighter grade), and render_preview again", len(misses), measured, strings.Join(parts, "; "))
	}
	if math.IsInf(kept, 1) {
		return c.pass(CheckCrushed, "%d shots rendered, none near black (no source analysis to compare with)", measured)
	}
	return c.pass(CheckCrushed, "%d shots rendered; the darkest keeps %.0f %% of its source's p90 luma (at least %.0f %%)", measured, kept*100, crushedRatio*100)
}

func (c *checker) beatSync() Check {
	var beats []int64
	for _, m := range c.in.Spec.Markers {
		if m.Kind == timeline.MarkerBeat || m.Kind == timeline.MarkerDownbeat {
			beats = append(beats, m.AtMs)
		}
	}
	cuts := len(c.items) - 1
	if cuts < 1 {
		return c.skip(CheckBeatSync, "there are no cuts")
	}
	if len(beats) == 0 {
		if c.lim.beatGrid {
			return c.fail(CheckBeatSync, "there is no beat grid; write one with snap_cuts_to_beats (a bpm from 120 to 128, first_beat_ms 0, write_markers true) so the cuts land on the beat")
		}
		return c.skip(CheckBeatSync, "no beat grid is expected (a %s video, or a plan with tempo 0)", c.typ)
	}
	slices.Sort(beats)
	tol := int64(beatToleranceFrames) * 1000 / int64(max(c.in.Spec.Output.FPS, 1))
	on := 0
	var off []string
	for i := 1; i < len(c.items); i++ {
		at := c.items[i].AtMs
		j, _ := slices.BinarySearch(beats, at)
		d := int64(math.MaxInt64)
		if j < len(beats) {
			d = min(d, beats[j]-at)
		}
		if j > 0 {
			d = min(d, at-beats[j-1])
		}
		if d <= tol {
			on++
		} else if len(off) < 3 {
			off = append(off, fmt.Sprintf("%s at %d ms is %d ms off", c.items[i].ID, at, d))
		}
	}
	c.m.CutsOnBeat = on
	if float64(on)/float64(cuts) < beatShareMin {
		return c.fail(CheckBeatSync, "%d of %d cuts are on a beat (within %d ms); off: %s. Make each shot's length whole beats (set_speed to source ms / (beats x beat ms), a ramped shot by trim_item), then snap_cuts_to_beats once with max_shift_ms 80", on, cuts, tol, strings.Join(off, "; "))
	}
	return c.pass(CheckBeatSync, "%d of %d cuts are on a beat (within %d ms)", on, cuts, tol)
}

// Text writes the report as the editor reads it.
func (r Report) Text() string {
	pass, fail, skip := r.Counts()
	var b strings.Builder
	target := ""
	if r.TargetS > 0 {
		target = fmt.Sprintf(", target %d s", r.TargetS)
	}
	fmt.Fprintf(&b, "check_edit for version %d (%s%s): %d of %d checks pass, %d fail, %d do not apply.\n", r.Version, r.ContentType, target, pass, pass+fail, fail, skip)
	for _, c := range r.Checks {
		label := map[CheckStatus]string{CheckPass: "PASS", CheckFail: "FAIL", CheckSkip: "n/a "}[c.Status]
		fmt.Fprintf(&b, "%s %s: %s\n", label, c.ID, c.Detail)
	}
	return strings.TrimRight(b.String(), "\n")
}
